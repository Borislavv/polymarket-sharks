package postgres

import (
	"context"
	"encoding/json"
	"time"
)

const (
	EvidenceSourceHolders         = "holders"
	EvidenceSourcePositionsVolume = "positions_by_volume"
	EvidenceSourcePositionsPNL    = "positions_by_pnl"
	EvidenceSourceLeaderboard     = "leaderboard"
)

type CandidateEvidence struct {
	WalletID     string
	MarketID     string  // optional
	Source       string  // one of EvidenceSource*
	SourceRank   int     // 1-based rank from upstream sort
	CurrentValue float64 // from /positions / market-positions
	TotalBought  float64
	CashPnL      float64
	RealizedPnL  float64
	PercentPnL   float64
	HolderAmount float64 // from /holders
	Snapshot     map[string]any
	Raw          []byte
	ObservedAt   time.Time
}

// InsertCandidateEvidence writes a single evidence row. Caller chooses
// source and rank. Cheap append-only insert; cleanup by retention job.
func (s *Store) InsertCandidateEvidence(ctx context.Context, e CandidateEvidence) error {
	if e.Snapshot == nil {
		e.Snapshot = map[string]any{}
	}
	snap, _ := json.Marshal(e.Snapshot)
	if e.Raw == nil {
		e.Raw = []byte(`{}`)
	}
	if e.ObservedAt.IsZero() {
		e.ObservedAt = time.Now()
	}
	const q = `
		INSERT INTO wallet_candidate_evidence
		    (wallet_id, market_id, source, source_rank,
		     current_value, total_bought, cash_pnl, realized_pnl, percent_pnl, holder_amount,
		     evidence_snapshot, observed_at, raw)
		VALUES
		    ($1::uuid, NULLIF($2,'')::uuid, $3, NULLIF($4,0),
		     NULLIF($5,0), NULLIF($6,0), $7, $8, $9, NULLIF($10,0),
		     $11::jsonb, $12, $13::jsonb)`
	_, err := s.Pool.Exec(ctx, q,
		e.WalletID, e.MarketID, e.Source, e.SourceRank,
		e.CurrentValue, e.TotalBought, e.CashPnL, e.RealizedPnL, e.PercentPnL, e.HolderAmount,
		string(snap), e.ObservedAt, string(e.Raw))
	return err
}

// HasProfitEvidence returns true if any candidate-evidence row for the
// wallet shows positive cash_pnl OR realized_pnl. Used by scoring to
// confirm a top-holder also has a profit signal.
func (s *Store) HasProfitEvidence(ctx context.Context, walletID string) (bool, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM wallet_candidate_evidence
		WHERE wallet_id = $1::uuid
		  AND (COALESCE(cash_pnl,0) > 0 OR COALESCE(realized_pnl,0) > 0)
	`, walletID).Scan(&n)
	return n > 0, err
}
