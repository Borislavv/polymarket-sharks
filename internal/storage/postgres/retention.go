package postgres

import (
	"context"
	"fmt"
)

type RetentionDeleteResult struct {
	Table  string
	Reason string
	Rows   int64
}

type ClosedPositionDedupResult struct {
	RowsScanned  int64
	RowsDeleted  int64
	MaxScannedID int64
}

func (s *Store) CountRetainedTableRows(ctx context.Context, table string) (int64, error) {
	if !isRetainedTable(table) {
		return 0, fmt.Errorf("retention: unsupported table %q", table)
	}
	q := fmt.Sprintf("SELECT count(*)::bigint FROM %s", table)
	var rows int64
	if err := s.Pool.QueryRow(ctx, q).Scan(&rows); err != nil {
		return 0, err
	}
	return rows, nil
}

func (s *Store) PruneWalletClosedPositionDuplicateBatch(ctx context.Context, limit int, afterID int64) (ClosedPositionDedupResult, error) {
	var res ClosedPositionDedupResult
	if limit <= 0 {
		return res, nil
	}
	const q = `
		WITH sample AS MATERIALIZED (
			SELECT id, wallet_id, condition_id, outcome, observed_at
			FROM wallet_closed_positions old
			WHERE old.id > $2
			ORDER BY old.id ASC
			LIMIT $1
		),
		doomed AS MATERIALIZED (
			SELECT s.id
			FROM sample s
			WHERE EXISTS (
				SELECT 1
				FROM wallet_closed_positions newer
				WHERE newer.wallet_id = s.wallet_id
				  AND newer.condition_id = s.condition_id
				  AND newer.outcome IS NOT DISTINCT FROM s.outcome
				  AND (
						newer.observed_at > s.observed_at OR
						(newer.observed_at = s.observed_at AND newer.id > s.id)
				  )
			)
		),
		del AS (
			DELETE FROM wallet_closed_positions w
			USING doomed d
			WHERE w.id = d.id
			RETURNING w.id
		)
		SELECT
			(SELECT count(*) FROM sample)::bigint,
			COALESCE((SELECT max(id) FROM sample), 0)::bigint,
			(SELECT count(*) FROM del)::bigint`
	err := s.Pool.QueryRow(ctx, q, limit, afterID).Scan(&res.RowsScanned, &res.MaxScannedID, &res.RowsDeleted)
	return res, err
}

func (s *Store) PruneWalletClosedPositionsOldestNonWatchlist(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	const q = `
		WITH doomed AS MATERIALIZED (
			SELECT w.id
			FROM wallet_closed_positions w
			LEFT JOIN wallet_watchlist ww ON ww.wallet_id = w.wallet_id
			WHERE ww.wallet_id IS NULL
			ORDER BY COALESCE(w.last_seen_at, w.observed_at) ASC NULLS FIRST, w.id ASC
			LIMIT $1
		)
		DELETE FROM wallet_closed_positions w
		USING doomed d
		WHERE w.id = d.id`
	ct, err := s.Pool.Exec(ctx, q, limit)
	return ct.RowsAffected(), err
}

func (s *Store) PruneMarketPriceSamplesOldest(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	const q = `
		WITH doomed AS MATERIALIZED (
			SELECT id
			FROM market_price_samples
			ORDER BY sampled_at ASC, id ASC
			LIMIT $1
		)
		DELETE FROM market_price_samples m
		USING doomed d
		WHERE m.id = d.id`
	ct, err := s.Pool.Exec(ctx, q, limit)
	return ct.RowsAffected(), err
}

func (s *Store) PruneHolderSnapshotsOldest(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	const q = `
		WITH doomed AS MATERIALIZED (
			SELECT id
			FROM holder_snapshots
			ORDER BY snapshot_at ASC, id ASC
			LIMIT $1
		)
		DELETE FROM holder_snapshots h
		USING doomed d
		WHERE h.id = d.id`
	ct, err := s.Pool.Exec(ctx, q, limit)
	return ct.RowsAffected(), err
}

func (s *Store) PruneCandidateEvidenceOldestNonWatchlist(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	const q = `
		WITH doomed AS MATERIALIZED (
			SELECT e.id
			FROM wallet_candidate_evidence e
			LEFT JOIN wallet_watchlist ww ON ww.wallet_id = e.wallet_id
			WHERE ww.wallet_id IS NULL
			ORDER BY e.observed_at ASC, e.id ASC
			LIMIT $1
		)
		DELETE FROM wallet_candidate_evidence e
		USING doomed d
		WHERE e.id = d.id`
	ct, err := s.Pool.Exec(ctx, q, limit)
	return ct.RowsAffected(), err
}

func (s *Store) PruneWalletScoresOldestNonWatchlist(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	const q = `
		WITH latest AS MATERIALIZED (
			SELECT DISTINCT ON (wallet_id, strategy) id
			FROM wallet_scores
			ORDER BY wallet_id, strategy, calculated_at DESC, id DESC
		),
		doomed AS MATERIALIZED (
			SELECT ws.id
			FROM wallet_scores ws
			LEFT JOIN wallet_watchlist ww ON ww.wallet_id = ws.wallet_id
			WHERE ww.wallet_id IS NULL
			  AND ws.promote = false
			  AND NOT EXISTS (SELECT 1 FROM latest l WHERE l.id = ws.id)
			ORDER BY ws.calculated_at ASC, ws.id ASC
			LIMIT $1
		)
		DELETE FROM wallet_scores ws
		USING doomed d
		WHERE ws.id = d.id`
	ct, err := s.Pool.Exec(ctx, q, limit)
	return ct.RowsAffected(), err
}

func isRetainedTable(table string) bool {
	switch table {
	case "wallet_closed_positions",
		"market_price_samples",
		"holder_snapshots",
		"wallet_candidate_evidence",
		"wallet_scores":
		return true
	default:
		return false
	}
}
