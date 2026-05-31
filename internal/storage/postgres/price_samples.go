package postgres

import (
	"context"
	"time"
)

type PriceSample struct {
	MarketID  string
	TokenID   string // optional
	Outcome   string
	Price     float64
	Midpoint  float64
	BestBid   float64
	BestAsk   float64
	SampledAt time.Time
	Source    string
	Raw       []byte
}

func (s *Store) InsertPriceSample(ctx context.Context, p PriceSample) error {
	if p.Raw == nil {
		p.Raw = []byte(`{}`)
	}
	const q = `
		INSERT INTO market_price_samples
		    (market_id, token_id, outcome, price, midpoint, best_bid, best_ask,
		     sampled_at, source, raw)
		VALUES ($1::uuid, NULLIF($2,'')::uuid, NULLIF($3,''), $4, $5, $6, $7,
		        $8, $9, $10::jsonb)`
	_, err := s.Pool.Exec(ctx, q,
		p.MarketID, p.TokenID, p.Outcome,
		zeroToNullF(p.Price), zeroToNullF(p.Midpoint),
		zeroToNullF(p.BestBid), zeroToNullF(p.BestAsk),
		p.SampledAt, p.Source, string(p.Raw))
	return err
}

func zeroToNullF(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}

// NearestSampleAfter returns the closest price sample for marketID with
// sampled_at >= notBefore. Used by CLV proxy to find post-trade drift.
func (s *Store) NearestSampleAfter(ctx context.Context, marketID, outcome string, notBefore time.Time) (float64, time.Time, error) {
	var price float64
	var sampledAt time.Time
	err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(price, midpoint, (best_bid+best_ask)/2, 0), sampled_at
		FROM market_price_samples
		WHERE market_id = $1::uuid
		  AND ($2 = '' OR outcome = $2)
		  AND sampled_at >= $3
		ORDER BY sampled_at ASC
		LIMIT 1
	`, marketID, outcome, notBefore).Scan(&price, &sampledAt)
	if err != nil {
		return 0, time.Time{}, err
	}
	return price, sampledAt, nil
}
