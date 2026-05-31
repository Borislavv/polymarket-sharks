package postgres

import (
	"context"
	"time"
)

type HolderSnapshot struct {
	MarketID           string
	TokenID            string // optional
	WalletID           string
	OutcomeIndex       int
	Amount             float64
	Rank               int
	PctOutcomeSnapshot float64
	PctValid           bool
	Source             string
	Raw                []byte
	SnapshotAt         time.Time
}

func (s *Store) InsertHolderSnapshot(ctx context.Context, h HolderSnapshot) error {
	if h.Raw == nil {
		h.Raw = []byte(`{}`)
	}
	var pct any
	if h.PctValid {
		pct = h.PctOutcomeSnapshot
	}
	var tokenID any
	if h.TokenID != "" {
		tokenID = h.TokenID
	}
	const q = `
		INSERT INTO holder_snapshots
		    (market_id, token_id, wallet_id, outcome_index, amount, rank,
		     pct_outcome_snapshot, snapshot_at, source, raw)
		VALUES
		    ($1::uuid, $2::uuid, $3::uuid, $4, $5, NULLIF($6,0), $7, $8, $9, $10::jsonb)`
	_, err := s.Pool.Exec(ctx, q,
		h.MarketID, tokenID, h.WalletID, h.OutcomeIndex, h.Amount, h.Rank,
		pct, h.SnapshotAt, h.Source, string(h.Raw))
	return err
}
