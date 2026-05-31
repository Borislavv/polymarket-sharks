package walletintel

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// ReconcileInsiderStreaks audits every active/watch_only insider_like row.
// If wallet_closed_positions now shows any losing closed position, the row is
// moved to status='streak_broken' so the watched-wallet worker no longer
// emits insider-like alerts for it. The check is read-only beyond the demote.
func ReconcileInsiderStreaks(ctx context.Context, store *postgres.Store, log *slog.Logger) (int, error) {
	rows, err := store.Pool.Query(ctx, `
		SELECT wallet_id::text, COALESCE(reason_codes,'{}'::text[]),
		       COALESCE(feature_snapshot::text,'{}')
		FROM wallet_watchlist
		WHERE class = 'insider_like'
		  AND status IN ('active','watch_only')
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type row struct {
		WID, Snapshot string
		Reasons       []string
	}
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.WID, &r.Reasons, &r.Snapshot); err != nil {
			return 0, err
		}
		rs = append(rs, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	demoted := 0
	for _, r := range rs {
		stats, err := store.GetHistoricalCloseStats(ctx, r.WID)
		if err != nil {
			continue
		}
		if stats.LosingCount == 0 {
			continue
		}
		var snap map[string]any
		_ = json.Unmarshal([]byte(r.Snapshot), &snap)
		if snap == nil {
			snap = map[string]any{}
		}
		snap["streak_broken_at_check"] = true
		snap["losing_closed_positions_at_demote"] = stats.LosingCount
		bs, _ := json.Marshal(snap)
		newReasons := append([]string(nil), r.Reasons...)
		newReasons = append(newReasons, "STREAK_BROKEN", "DEMOTED_UNACTUAL")
		if _, err := store.Pool.Exec(ctx, `
			UPDATE wallet_watchlist
			SET status = 'streak_broken',
			    reason_codes = $2,
			    feature_snapshot = $3::jsonb,
			    updated_at = now()
			WHERE wallet_id = $1::uuid
		`, r.WID, newReasons, string(bs)); err != nil {
			if log != nil {
				log.Warn("insider streak demote", "wallet_id", r.WID, "err", err)
			}
			continue
		}
		demoted++
	}
	if demoted > 0 {
		metrics.Add("insider_streak_broken_total", int64(demoted))
	}
	if log != nil {
		log.Info("insider streak reconciliation completed",
			"demoted", demoted, "audited", len(rs))
	}
	return demoted, nil
}
