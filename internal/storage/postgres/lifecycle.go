package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type LifecycleRow struct {
	ID                  string
	WalletID            string
	MarketID            string
	ConditionID         string
	Outcome             string
	OpenedDirection     string
	OpenedSide          string
	OpenTradeID         string
	OpenTransactionHash string
	OpenNotional        float64
	OpenPrice           float64
	OpenSize            float64
	Status              string // open | partially_exited | closed
	ExitedSize          float64
	ExitNotional        float64
	AvgExitPrice        float64 // 0 if unknown
	RealizedPnL         float64 // 0 if unknown
	RealizedPnLKnown    bool
	OpenedAt            time.Time
	LastExitAt          time.Time
	ClosedAt            time.Time
	FeatureSnapshot     map[string]any
}

// OpenLifecycle inserts a new lifecycle row for an opening bet. Idempotent
// on (wallet_id, condition_id, outcome, open_transaction_hash). Returns
// (id, insertedNew).
func (s *Store) OpenLifecycle(ctx context.Context, r LifecycleRow) (string, bool, error) {
	if r.FeatureSnapshot == nil {
		r.FeatureSnapshot = map[string]any{}
	}
	snap, _ := json.Marshal(r.FeatureSnapshot)
	const q = `
		INSERT INTO watched_position_lifecycle
		    (wallet_id, market_id, condition_id, outcome, opened_direction, opened_side,
		     open_trade_id, open_transaction_hash, open_notional, open_price, open_size,
		     status, opened_at, feature_snapshot)
		VALUES
		    ($1::uuid, NULLIF($2,'')::uuid, $3, $4, $5, $6,
		     NULLIF($7,'')::uuid, $8, $9, $10, NULLIF($11,0),
		     'open', $12, $13::jsonb)
		ON CONFLICT (wallet_id, condition_id, outcome, open_transaction_hash) DO NOTHING
		RETURNING id::text`
	var id string
	err := s.Pool.QueryRow(ctx, q,
		r.WalletID, r.MarketID, r.ConditionID, r.Outcome, r.OpenedDirection, r.OpenedSide,
		r.OpenTradeID, r.OpenTransactionHash, r.OpenNotional, r.OpenPrice, r.OpenSize,
		r.OpenedAt, string(snap),
	).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err2 := s.Pool.QueryRow(ctx, `
			SELECT id::text FROM watched_position_lifecycle
			WHERE wallet_id=$1::uuid AND condition_id=$2 AND outcome=$3 AND open_transaction_hash=$4
		`, r.WalletID, r.ConditionID, r.Outcome, r.OpenTransactionHash).Scan(&id)
		if err2 != nil {
			return "", false, err2
		}
		return id, false, nil
	}
	return "", false, err
}

// FindOpenLifecycle returns the most recent open/partially_exited lifecycle
// row matching wallet+condition+outcome. Returns (row, found, err).
func (s *Store) FindOpenLifecycle(ctx context.Context, walletID, conditionID, outcome string) (LifecycleRow, bool, error) {
	var r LifecycleRow
	var marketID *string
	var lastExit, closedAt *time.Time
	var avgExit, realized *float64
	var openSize *float64
	err := s.Pool.QueryRow(ctx, `
		SELECT id::text, wallet_id::text, market_id::text, condition_id, outcome,
		       opened_direction, opened_side, COALESCE(open_transaction_hash,''),
		       open_notional, open_price, open_size, status,
		       exited_size, exit_notional, avg_exit_price, realized_pnl,
		       opened_at, last_exit_at, closed_at
		FROM watched_position_lifecycle
		WHERE wallet_id=$1::uuid AND condition_id=$2 AND outcome=$3
		  AND status IN ('open','partially_exited')
		ORDER BY opened_at DESC
		LIMIT 1
	`, walletID, conditionID, outcome).Scan(
		&r.ID, &r.WalletID, &marketID, &r.ConditionID, &r.Outcome,
		&r.OpenedDirection, &r.OpenedSide, &r.OpenTransactionHash,
		&r.OpenNotional, &r.OpenPrice, &openSize, &r.Status,
		&r.ExitedSize, &r.ExitNotional, &avgExit, &realized,
		&r.OpenedAt, &lastExit, &closedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if marketID != nil {
		r.MarketID = *marketID
	}
	if openSize != nil {
		r.OpenSize = *openSize
	}
	if avgExit != nil {
		r.AvgExitPrice = *avgExit
	}
	if realized != nil {
		r.RealizedPnL = *realized
		r.RealizedPnLKnown = true
	}
	if lastExit != nil {
		r.LastExitAt = *lastExit
	}
	if closedAt != nil {
		r.ClosedAt = *closedAt
	}
	return r, true, nil
}

type ExitRecord struct {
	LifecycleID     string
	WalletTradeID   string
	TransactionHash string
	Side            string
	Price           float64
	Size            float64
	Notional        float64
	PnLEstimate     float64
	PnLKnown        bool
	DetectedAt      time.Time
}

// RecordExitAndUpdateLifecycle inserts an exit row and updates the
// parent lifecycle aggregate. Returns (insertedExitID, newStatus, inserted).
func (s *Store) RecordExitAndUpdateLifecycle(ctx context.Context, e ExitRecord, fullCloseTolerance float64) (string, string, bool, error) {
	// idempotent: (lifecycle_id, transaction_hash) unique
	var exitID string
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO watched_position_exits
		    (lifecycle_id, wallet_trade_id, transaction_hash, side, price, size, notional, pnl_estimate, detected_at)
		VALUES ($1::uuid, NULLIF($2,'')::uuid, $3, $4, NULLIF($5,0), NULLIF($6,0), NULLIF($7,0),
		        CASE WHEN $9 THEN $8::numeric ELSE NULL::numeric END, $10)
		ON CONFLICT (lifecycle_id, transaction_hash) DO NOTHING
		RETURNING id::text
	`, e.LifecycleID, e.WalletTradeID, e.TransactionHash, e.Side,
		e.Price, e.Size, e.Notional, e.PnLEstimate, e.PnLKnown, e.DetectedAt).Scan(&exitID)
	if errors.Is(err, pgx.ErrNoRows) {
		// duplicate
		var status string
		s.Pool.QueryRow(ctx, `
			SELECT status FROM watched_position_lifecycle WHERE id=(
			  SELECT lifecycle_id FROM watched_position_exits WHERE lifecycle_id=$1::uuid AND transaction_hash=$2)
		`, e.LifecycleID, e.TransactionHash).Scan(&status)
		return "", status, false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	// Recompute aggregate
	var openSize *float64
	var exitedSize, exitNotional float64
	var avgExit *float64
	var status string
	if err := s.Pool.QueryRow(ctx, `
		SELECT open_size, status FROM watched_position_lifecycle WHERE id=$1::uuid
	`, e.LifecycleID).Scan(&openSize, &status); err != nil {
		return exitID, status, true, err
	}
	if err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(size),0), COALESCE(SUM(notional),0),
		       CASE WHEN COALESCE(SUM(size),0)>0 THEN SUM(notional)/SUM(size) ELSE NULL END
		FROM watched_position_exits WHERE lifecycle_id=$1::uuid
	`, e.LifecycleID).Scan(&exitedSize, &exitNotional, &avgExit); err != nil {
		return exitID, status, true, err
	}
	newStatus := "partially_exited"
	closedAt := "NULL"
	if openSize != nil && *openSize > 0 {
		remaining := *openSize - exitedSize
		if remaining <= *openSize*fullCloseTolerance {
			newStatus = "closed"
			closedAt = "now()"
		}
	} else if e.Size == 0 {
		// no size info — keep partially_exited until manual reconciliation
		newStatus = "partially_exited"
	}
	upd := `
		UPDATE watched_position_lifecycle
		SET status = $2,
		    exited_size = $3,
		    exit_notional = $4,
		    avg_exit_price = $5,
		    last_exit_at = $6,
		    closed_at = ` + closedAt + `,
		    updated_at = now()
		WHERE id = $1::uuid`
	if _, err := s.Pool.Exec(ctx, upd, e.LifecycleID, newStatus, exitedSize, exitNotional, avgExit, e.DetectedAt); err != nil {
		return exitID, status, true, err
	}
	return exitID, newStatus, true, nil
}
