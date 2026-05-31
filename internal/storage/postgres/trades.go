package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type WalletTrade struct {
	ID              string
	TransactionHash string
	WalletID        string
	MarketID        string // optional
	EventSlug       string
	ConditionID     string
	Outcome         string
	Side            string
	Direction       string
	Price           float64
	Size            float64
	UsdcSize        float64
	Timestamp       time.Time
	Source          string
	Raw             []byte
}

// InsertWalletTrade is idempotent on (transaction_hash, wallet, condition,
// outcome, side). Returns (id, inserted, error). inserted=false means
// duplicate — caller can skip downstream side effects.
func (s *Store) InsertWalletTrade(ctx context.Context, t WalletTrade) (string, bool, error) {
	if t.Raw == nil {
		t.Raw = []byte(`{}`)
	}
	const q = `
		INSERT INTO wallet_trades
		    (transaction_hash, wallet_id, market_id, event_slug, condition_id,
		     outcome, side, direction, price, size, usdc_size, timestamp, source, raw)
		VALUES
		    ($1, $2::uuid, NULLIF($3,'')::uuid, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::jsonb)
		ON CONFLICT (transaction_hash, wallet_id, condition_id, outcome, side) DO NOTHING
		RETURNING id::text`
	var id string
	err := s.Pool.QueryRow(ctx, q,
		t.TransactionHash, t.WalletID, t.MarketID, t.EventSlug, t.ConditionID,
		t.Outcome, t.Side, t.Direction, t.Price, t.Size, t.UsdcSize, t.Timestamp,
		t.Source, string(t.Raw),
	).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// duplicate — fetch existing id
		err2 := s.Pool.QueryRow(ctx, `
			SELECT id::text FROM wallet_trades
			WHERE transaction_hash=$1 AND wallet_id=$2::uuid AND condition_id=$3 AND outcome=$4 AND side=$5
		`, t.TransactionHash, t.WalletID, t.ConditionID, t.Outcome, t.Side).Scan(&id)
		if err2 != nil {
			return "", false, err2
		}
		return id, false, nil
	}
	return "", false, err
}
