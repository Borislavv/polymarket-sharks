package postgres

import (
	"context"
	"encoding/json"
	"time"
)

// ClosedPositionRow is the storage-level representation of a wallet's
// closed-position record. One row per (wallet_id, condition_id, outcome):
// each refresh either updates the existing row in place or inserts a new
// one. Scoring reads the same row (no longer DISTINCT-ON history).
type ClosedPositionRow struct {
	WalletID           string
	ConditionID        string
	MarketID           string // empty when unknown
	EventSlug          string
	Outcome            string
	OutcomeIndex       int
	TotalBought        float64
	RealizedPnL        float64
	AvgPrice           float64
	CurrentValue       float64
	PercentPnL         float64
	PercentRealizedPnL float64
	SizeAtObservation  float64
	IsClosed           bool
	ClosedAt           *time.Time
	Raw                []byte
}

// ClosedPositionUpsertResult counts the work done by UpsertClosedPositions.
type ClosedPositionUpsertResult struct {
	Inserted int
	Updated  int
}

// UpsertClosedPositions writes a batch of closed-position records under the
// latest-state model. For each row it updates the existing row matching
// (wallet_id, condition_id, outcome) or inserts when none exists. The CTE
// form below does NOT depend on a unique constraint, so it remains correct
// against the production table both before and after the dedup cleanup
// adds uq_wcp_wallet_position. first_seen_at is preserved across updates;
// last_seen_at and observed_at advance every cycle.
func (s *Store) UpsertClosedPositions(ctx context.Context, walletID string, rows []ClosedPositionRow) (ClosedPositionUpsertResult, error) {
	var res ClosedPositionUpsertResult
	if len(rows) == 0 {
		return res, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Update-or-insert in a single statement. The UPDATE branch matches every
	// row for the key (so duplicate legacy rows are kept in sync until the
	// cleanup script removes them); the INSERT runs only when no row existed.
	const q = `
		WITH upd AS (
		    UPDATE wallet_closed_positions
		    SET market_id            = COALESCE(NULLIF($3,'')::uuid, market_id),
		        event_slug           = $4,
		        outcome_index        = $6,
		        total_bought         = $7,
		        realized_pnl         = $8,
		        avg_price            = $9,
		        current_value        = $10,
		        percent_pnl          = $11,
		        percent_realized_pnl = $12,
		        size_at_observation  = $13,
		        is_closed            = $14,
		        closed_at            = $15,
		        raw                  = $16::jsonb,
		        observed_at          = now(),
		        last_seen_at         = now(),
		        first_seen_at        = COALESCE(first_seen_at, now())
		    WHERE wallet_id = $1::uuid
		      AND condition_id = $2
		      AND outcome = $5
		    RETURNING 1
		),
		ins AS (
		    INSERT INTO wallet_closed_positions
		        (wallet_id, condition_id, market_id, event_slug, outcome, outcome_index,
		         total_bought, realized_pnl, avg_price, current_value,
		         percent_pnl, percent_realized_pnl,
		         size_at_observation, is_closed, closed_at, raw,
		         observed_at, first_seen_at, last_seen_at)
		    SELECT $1::uuid, $2, NULLIF($3,'')::uuid, $4, $5, $6,
		           $7, $8, $9, $10, $11, $12,
		           $13, $14, $15, $16::jsonb,
		           now(), now(), now()
		    WHERE NOT EXISTS (SELECT 1 FROM upd)
		    RETURNING 1
		)
		SELECT
		    (SELECT count(*) FROM upd) AS updated,
		    (SELECT count(*) FROM ins) AS inserted`
	for _, r := range rows {
		raw := r.Raw
		if len(raw) == 0 {
			raw = []byte(`{}`)
		}
		var updated, inserted int
		if err := tx.QueryRow(ctx, q,
			walletID, r.ConditionID, r.MarketID, r.EventSlug, r.Outcome, r.OutcomeIndex,
			r.TotalBought, r.RealizedPnL, r.AvgPrice, r.CurrentValue,
			r.PercentPnL, r.PercentRealizedPnL,
			r.SizeAtObservation, r.IsClosed, r.ClosedAt, string(raw),
		).Scan(&updated, &inserted); err != nil {
			return res, err
		}
		res.Updated += updated
		res.Inserted += inserted
	}
	if err := tx.Commit(ctx); err != nil {
		return res, err
	}
	return res, nil
}

// InsertClosedPositions is retained as a thin wrapper around UpsertClosedPositions
// so existing call sites keep working. The returned count is total rows
// touched (inserted + updated). Prefer UpsertClosedPositions in new code.
func (s *Store) InsertClosedPositions(ctx context.Context, walletID string, rows []ClosedPositionRow) (int, error) {
	res, err := s.UpsertClosedPositions(ctx, walletID, rows)
	return res.Inserted + res.Updated, err
}

// TouchClosedPositionsLastSeen advances last_seen_at and observed_at for a
// batch of (wallet, condition, outcome) keys without rewriting other fields.
// Used when the snapshot cache reports the payload is unchanged: we still
// want a heartbeat that the position is current, without paying the cost of
// a full row rewrite.
func (s *Store) TouchClosedPositionsLastSeen(ctx context.Context, walletID string, keys []ClosedPositionKey) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	const q = `
		UPDATE wallet_closed_positions
		SET observed_at = now(), last_seen_at = now()
		WHERE wallet_id = $1::uuid
		  AND condition_id = $2
		  AND outcome = $3`
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	touched := 0
	for _, k := range keys {
		ct, err := tx.Exec(ctx, q, walletID, k.ConditionID, k.Outcome)
		if err != nil {
			return touched, err
		}
		touched += int(ct.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return touched, err
	}
	return touched, nil
}

// ClosedPositionKey identifies a closed position uniquely within a wallet.
type ClosedPositionKey struct {
	ConditionID string
	Outcome     string
}

// HistoricalCloseStats is the aggregated truth that v4 shark scoring reads.
// Only positions marked IsClosed=true are considered; partial-close-only rows
// (size>0 AND realized_pnl!=0) contribute to ClosedCount and stats but stake
// is taken from total_bought (entry side, not exit side).
type HistoricalCloseStats struct {
	ClosedCount       int
	ProfitableCount   int
	LosingCount       int
	TotalBoughtClosed float64
	TotalRealizedPnL  float64
	AvgClosedStake    float64
	MedianClosedStake float64
	WinRate           float64
	ROI               float64
	MaxWin            float64
	MaxLoss           float64
	LastClosedAt      *time.Time
	LastObservedAt    *time.Time
}

// GetHistoricalCloseStats reads the latest snapshot per (condition, outcome)
// for the wallet and computes ROI/win-rate/avg-stake. Pure SQL; no per-row
// JSON parsing.
func (s *Store) GetHistoricalCloseStats(ctx context.Context, walletID string) (HistoricalCloseStats, error) {
	const q = `
		WITH latest AS (
		    SELECT DISTINCT ON (condition_id, outcome)
		           wallet_id, condition_id, outcome,
		           total_bought, realized_pnl, is_closed, closed_at, observed_at
		    FROM wallet_closed_positions
		    WHERE wallet_id = $1::uuid
		    ORDER BY condition_id, outcome, observed_at DESC
		)
		SELECT
		    count(*) FILTER (WHERE is_closed)                                       AS closed_count,
		    count(*) FILTER (WHERE is_closed AND realized_pnl > 0)                  AS profitable_count,
		    count(*) FILTER (WHERE is_closed AND realized_pnl < 0)                  AS losing_count,
		    COALESCE(sum(total_bought) FILTER (WHERE is_closed AND total_bought>0), 0) AS total_bought_closed,
		    COALESCE(sum(realized_pnl) FILTER (WHERE is_closed), 0)                 AS total_realized_pnl,
		    COALESCE(max(realized_pnl) FILTER (WHERE is_closed AND realized_pnl>0), 0) AS max_win,
		    COALESCE(min(realized_pnl) FILTER (WHERE is_closed AND realized_pnl<0), 0) AS max_loss,
		    max(closed_at) FILTER (WHERE is_closed)                                  AS last_closed_at,
		    max(observed_at)                                                         AS last_observed_at
		FROM latest`
	var st HistoricalCloseStats
	var lastClosed, lastObserved *time.Time
	err := s.Pool.QueryRow(ctx, q, walletID).Scan(
		&st.ClosedCount, &st.ProfitableCount, &st.LosingCount,
		&st.TotalBoughtClosed, &st.TotalRealizedPnL,
		&st.MaxWin, &st.MaxLoss,
		&lastClosed, &lastObserved,
	)
	if err != nil {
		return st, err
	}
	st.LastClosedAt = lastClosed
	st.LastObservedAt = lastObserved
	if st.ClosedCount > 0 && st.TotalBoughtClosed > 0 {
		st.AvgClosedStake = st.TotalBoughtClosed / float64(st.ClosedCount)
		st.ROI = st.TotalRealizedPnL / st.TotalBoughtClosed
	}
	if st.ClosedCount > 0 {
		st.WinRate = float64(st.ProfitableCount) / float64(st.ClosedCount)
	}
	// Median computed in a second cheap roundtrip to keep the SQL simple.
	if st.ClosedCount > 1 {
		const qm = `
			WITH latest AS (
			    SELECT DISTINCT ON (condition_id, outcome) total_bought
			    FROM wallet_closed_positions
			    WHERE wallet_id = $1::uuid
			    ORDER BY condition_id, outcome, observed_at DESC
			)
			SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY total_bought)
			FROM latest WHERE total_bought > 0`
		var m float64
		if err := s.Pool.QueryRow(ctx, qm, walletID).Scan(&m); err == nil {
			st.MedianClosedStake = m
		}
	} else if st.ClosedCount == 1 {
		st.MedianClosedStake = st.AvgClosedStake
	}
	return st, nil
}

// BackfillRow is the per-wallet bookkeeping for historical backfill.
type BackfillRow struct {
	WalletID                string
	TradesFetched           int
	ClosedPositionsFetched  int
	TradesComplete          bool
	ClosedPositionsComplete bool
	LastBackfilledAt        *time.Time
	LastError               string
	RawStats                map[string]any
}

// UpsertBackfillRecord updates the wallet_history_backfills row.
func (s *Store) UpsertBackfillRecord(ctx context.Context, r BackfillRow) error {
	raw, err := json.Marshal(r.RawStats)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO wallet_history_backfills
			(wallet_id, trades_fetched, closed_positions_fetched,
			 trades_complete, closed_positions_complete,
			 last_backfilled_at, last_error, raw_stats, updated_at)
		VALUES
			($1::uuid, $2, $3, $4, $5, COALESCE($6, now()), NULLIF($7,''), $8::jsonb, now())
		ON CONFLICT (wallet_id) DO UPDATE SET
			trades_fetched            = EXCLUDED.trades_fetched,
			closed_positions_fetched  = EXCLUDED.closed_positions_fetched,
			trades_complete           = EXCLUDED.trades_complete,
			closed_positions_complete = EXCLUDED.closed_positions_complete,
			last_backfilled_at        = EXCLUDED.last_backfilled_at,
			last_error                = EXCLUDED.last_error,
			raw_stats                 = EXCLUDED.raw_stats,
			updated_at                = now()`
	_, err = s.Pool.Exec(ctx, q,
		r.WalletID, r.TradesFetched, r.ClosedPositionsFetched,
		r.TradesComplete, r.ClosedPositionsComplete,
		r.LastBackfilledAt, r.LastError, string(raw),
	)
	return err
}

// GetBackfillRecord returns existing record or zero-value with WalletID set.
func (s *Store) GetBackfillRecord(ctx context.Context, walletID string) (BackfillRow, bool, error) {
	const q = `
		SELECT trades_fetched, closed_positions_fetched,
		       trades_complete, closed_positions_complete,
		       last_backfilled_at, COALESCE(last_error,''), raw_stats::text
		FROM wallet_history_backfills
		WHERE wallet_id = $1::uuid`
	var (
		r       BackfillRow
		rawJSON string
	)
	r.WalletID = walletID
	err := s.Pool.QueryRow(ctx, q, walletID).Scan(
		&r.TradesFetched, &r.ClosedPositionsFetched,
		&r.TradesComplete, &r.ClosedPositionsComplete,
		&r.LastBackfilledAt, &r.LastError, &rawJSON,
	)
	if err != nil {
		// not found is fine — return zero record so callers can decide
		return r, false, nil
	}
	if rawJSON != "" {
		_ = json.Unmarshal([]byte(rawJSON), &r.RawStats)
	}
	return r, true, nil
}

// ListWalletsNeedingBackfill returns wallets that have evidence but no
// completed historical backfill yet. Bounded by limit.
func (s *Store) ListWalletsNeedingBackfill(ctx context.Context, limit int) ([]WalletBackfillTarget, error) {
	if limit <= 0 {
		limit = 50
	}
	const q = `
		SELECT w.id::text, w.proxy_wallet
		FROM wallets w
		LEFT JOIN wallet_history_backfills b ON b.wallet_id = w.id
		WHERE EXISTS (
		    SELECT 1 FROM wallet_candidate_evidence e WHERE e.wallet_id = w.id
		)
		AND (
		    b.wallet_id IS NULL
		    OR b.closed_positions_complete = false
		    OR b.last_backfilled_at IS NULL
		    OR b.last_backfilled_at < now() - interval '6 hours'
		)
		ORDER BY w.last_seen_at DESC NULLS LAST
		LIMIT $1`
	rows, err := s.Pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WalletBackfillTarget
	for rows.Next() {
		var t WalletBackfillTarget
		if err := rows.Scan(&t.WalletID, &t.ProxyWallet); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// WalletBackfillTarget is a thin pair returned by ListWalletsNeedingBackfill.
type WalletBackfillTarget struct {
	WalletID    string
	ProxyWallet string
}
