package walletintel

import (
	"context"
	"log/slog"

	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// ReconcileFailedSharks audits every active/watch_only shark against the
// latest wallet_scores row (strategy='shark_score', current score_version).
//
// Source of truth for gate evaluation is wallet_scores.promote, NOT the
// watchlist's feature_snapshot (which is an arbitration snapshot and does not
// contain scoring-gate keys like closed_positions_complete, roi, win_rate).
//
//   - latest score found, promote=true, version=current  → wallet stays active
//   - latest score found, promote=false, version=current → demote; reason_codes
//     from wallet_scores are used, not inferred
//   - latest score found, version stale                  → demote STALE_SCORE_VERSION
//   - no latest score row                                → mark needs_rescore; never
//     demote with PARTIAL_CLOSED_POSITION_HISTORY
func ReconcileFailedSharks(ctx context.Context, store *postgres.Store, _ SharkParams, log *slog.Logger) (int, error) {
	rows, err := store.Pool.Query(ctx, `
		SELECT wallet_id::text, COALESCE(score_version,''),
		       COALESCE(reason_codes, '{}'::text[])
		FROM wallet_watchlist
		WHERE class = 'shark'
		  AND status IN ('active','watch_only')
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type row struct {
		WID, Version string
		Reasons      []string
	}
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.WID, &r.Version, &r.Reasons); err != nil {
			return 0, err
		}
		rs = append(rs, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	demoted := 0
	for _, r := range rs {
		latest, err := store.GetLatestSharkScoreRow(ctx, r.WID)
		if err != nil {
			if log != nil {
				log.Warn("reconcile: failed to fetch latest score", "wallet_id", r.WID, "err", err)
			}
			continue
		}

		if !latest.Found {
			// No score row yet — queue for rescore, do not demote.
			if log != nil {
				log.Warn("reconcile: no scoring row found, marking needs_rescore",
					"wallet_id", r.WID)
			}
			_, _ = store.Pool.Exec(ctx, `
				UPDATE wallet_watchlist
				SET status='needs_rescore', updated_at=now()
				WHERE wallet_id=$1::uuid AND status IN ('active','watch_only')
			`, r.WID)
			continue
		}

		if latest.ScoreVersion != ScoreVersion {
			// Score is from a previous engine version — demote as stale.
			demote(ctx, store, log, r.WID, r.Reasons, "STALE_SCORE_VERSION")
			demoted++
			continue
		}

		if latest.Promote {
			// Still qualifies — no action.
			continue
		}

		// Scoring says promote=false: demote with the actual reason_codes from
		// wallet_scores, not from the watchlist arbitration snapshot.
		demote(ctx, store, log, r.WID, latest.ReasonCodes, "SCORE_GATE_FAILED")
		demoted++
	}

	if demoted > 0 {
		metrics.Add("shark_reconcile_failed_demoted_total", int64(demoted))
	}
	if log != nil {
		log.Info("shark hard-gate reconciliation completed",
			"score_version", ScoreVersion, "demoted", demoted, "audited", len(rs))
	}
	return demoted, nil
}

// demote writes class='rejected_unactual' / status='unactual' for one wallet.
func demote(ctx context.Context, store *postgres.Store, log *slog.Logger, walletID string, reasons []string, reconcileReason string) {
	newReasons := append(append([]string(nil), reasons...), "DEMOTED_UNACTUAL", reconcileReason)
	if _, err := store.Pool.Exec(ctx, `
		UPDATE wallet_watchlist
		SET class='rejected_unactual', status='unactual',
		    reason_codes=$2, updated_at=now()
		WHERE wallet_id=$1::uuid
	`, walletID, newReasons); err != nil {
		if log != nil {
			log.Warn("reconcile demote", "wallet_id", walletID, "err", err)
		}
	} else if log != nil {
		log.Info("reconcile: shark demoted",
			"wallet_id", walletID, "reason", reconcileReason)
	}
}

// RestoreIncorrectlyDemotedSharks re-evaluates wallets that were demoted to
// 'unactual' and checks whether their latest wallet_scores row still has
// promote=true with the current score_version. If so, they are restored to
// 'active'. This corrects the historical P0 bug where the reconcile read the
// wrong (arbitration) feature_snapshot and false-demoted valid sharks.
//
// Returns the count of wallets restored.
func RestoreIncorrectlyDemotedSharks(ctx context.Context, store *postgres.Store, log *slog.Logger) (int, error) {
	rows, err := store.Pool.Query(ctx, `
		SELECT wallet_id::text
		FROM wallet_watchlist
		WHERE status = 'unactual'
		  AND class IN ('shark', 'rejected_unactual')
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var wids []string
	for rows.Next() {
		var wid string
		if err := rows.Scan(&wid); err != nil {
			return 0, err
		}
		wids = append(wids, wid)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	restored := 0
	for _, wid := range wids {
		latest, err := store.GetLatestSharkScoreRow(ctx, wid)
		if err != nil || !latest.Found {
			continue
		}
		if latest.ScoreVersion != ScoreVersion || !latest.Promote {
			continue
		}
		// Latest score says promote=true with current version — restore to active.
		if _, err := store.Pool.Exec(ctx, `
			UPDATE wallet_watchlist
			SET class='shark', status='active', updated_at=now()
			WHERE wallet_id=$1::uuid
			  AND status = 'unactual'
		`, wid); err != nil {
			if log != nil {
				log.Warn("restore unactual shark", "wallet_id", wid, "err", err)
			}
			continue
		}
		restored++
		if log != nil {
			log.Info("shark restored from unactual to active",
				"wallet_id", wid, "score", latest.Score, "score_version", latest.ScoreVersion)
		}
	}

	metrics.Add("shark_restored_from_unactual_total", int64(restored))
	if log != nil {
		log.Info("unactual shark restore completed", "restored", restored, "checked", len(wids))
	}
	return restored, nil
}

// ReconcileStaleSharks marks rows whose score_version is not the current
// ScoreVersion as rejected_stale. The next backfill/scoring cycle re-evaluates
// them under v4 historical gates.
func ReconcileStaleSharks(ctx context.Context, store *postgres.Store, log *slog.Logger) (int, error) {
	rows, err := store.Pool.Query(ctx, `
		SELECT wallet_id::text, COALESCE(score_version,''), score, confidence,
		       COALESCE(reason_codes, '{}'::text[]), COALESCE(feature_snapshot::text,'{}')
		FROM wallet_watchlist
		WHERE class = 'shark'
		  AND status IN ('active','watch_only')
		  AND COALESCE(score_version,'') <> $1
	`, ScoreVersion)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type r struct {
		WID, PrevVer, Snapshot string
		Score                  int
		Conf                   float64
		Reasons                []string
	}
	var stale []r
	for rows.Next() {
		var x r
		if err := rows.Scan(&x.WID, &x.PrevVer, &x.Score, &x.Conf, &x.Reasons, &x.Snapshot); err != nil {
			return 0, err
		}
		stale = append(stale, x)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, x := range stale {
		newReasons := append(append([]string(nil), x.Reasons...), "STALE_SCORE_VERSION")
		if _, err := store.Pool.Exec(ctx, `
			UPDATE wallet_watchlist
			SET class = 'rejected_stale',
			    status = 'rejected',
			    score_version = $2,
			    reason_codes = $3,
			    updated_at = now()
			WHERE wallet_id = $1::uuid
		`, x.WID, ScoreVersion, newReasons); err != nil {
			if log != nil {
				log.Warn("reconcile demote", "wallet_id", x.WID, "err", err)
			}
			continue
		}
		n++
	}
	if log != nil {
		log.Info("shark stale-version reconciliation completed",
			"score_version", ScoreVersion, "stale_demoted", n)
	}
	if n > 0 {
		metrics.Add("shark_reconcile_demoted_total", int64(n))
	}
	return n, nil
}
