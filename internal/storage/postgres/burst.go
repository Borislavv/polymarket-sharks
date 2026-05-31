package postgres

import (
	"context"
	"time"
)

// BurstCandidate is one wallet's recent entry bets in a window.
type BurstCandidate struct {
	WalletID        string
	ProxyWallet     string
	Pseudonym       string
	ProfileSlug     string
	Class           string
	BetsCount       int
	DistinctMarkets int
	TotalNotional   float64
	WeightedPrice   float64
	FirstAt         time.Time
	LastAt          time.Time
	Markets         []BurstMarketRow
}

type BurstMarketRow struct {
	MarketID    string
	MarketSlug  string
	MarketTitle string
	Direction   string
	Notional    float64
	BetCount    int
}

// FindBurstCandidates returns per-wallet aggregates of entry bets within
// `window` ending at `now`, restricted to wallets with active watchlist
// status. Aggregation is in SQL to keep it cheap.
func (s *Store) FindBurstCandidates(ctx context.Context, now time.Time, window time.Duration, minBets, minMarkets int, currentScoreVersion string) ([]BurstCandidate, error) {
	since := now.Add(-window)
	rows, err := s.Pool.Query(ctx, `
		WITH win AS (
		    SELECT wb.wallet_id, wb.market_id, wb.direction,
		           wb.notional, wb.price, wb.detected_at
		    FROM watched_bets wb
		    JOIN wallet_watchlist ww ON ww.wallet_id = wb.wallet_id
		    WHERE wb.bet_kind = 'entry'
		      AND wb.detected_at >= $1
		      AND ww.status = 'active'
		      AND ww.class = 'shark'
		      AND ww.score_version = $4
		)
		SELECT wallet_id::text,
		       count(*) AS bets_count,
		       count(DISTINCT market_id) AS distinct_markets,
		       COALESCE(sum(notional),0) AS total_notional,
		       CASE WHEN sum(notional) > 0
		            THEN sum(notional * price) / sum(notional)
		            ELSE 0 END AS weighted_price,
		       min(detected_at) AS first_at,
		       max(detected_at) AS last_at
		FROM win
		GROUP BY wallet_id
		HAVING count(*) >= $2 OR count(DISTINCT market_id) >= $3
	`, since, minBets, minMarkets, currentScoreVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BurstCandidate
	for rows.Next() {
		var c BurstCandidate
		if err := rows.Scan(&c.WalletID, &c.BetsCount, &c.DistinctMarkets,
			&c.TotalNotional, &c.WeightedPrice, &c.FirstAt, &c.LastAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// hydrate wallet metadata + per-market rows
	for i := range out {
		s.Pool.QueryRow(ctx, `
			SELECT w.proxy_wallet, COALESCE(w.pseudonym,''), COALESCE(w.profile_slug,''),
			       COALESCE(ww.class,'')
			FROM wallets w LEFT JOIN wallet_watchlist ww ON ww.wallet_id = w.id
			WHERE w.id = $1::uuid
		`, out[i].WalletID).Scan(&out[i].ProxyWallet, &out[i].Pseudonym, &out[i].ProfileSlug, &out[i].Class)

		mr, err := s.Pool.Query(ctx, `
			SELECT COALESCE(wb.market_id::text,''),
			       COALESCE(m.slug,''), COALESCE(m.question,''),
			       wb.direction, sum(wb.notional), count(*)
			FROM watched_bets wb
			LEFT JOIN markets m ON m.id = wb.market_id
			WHERE wb.bet_kind = 'entry' AND wb.detected_at >= $1
			  AND wb.wallet_id = $2::uuid
			GROUP BY wb.market_id, m.slug, m.question, wb.direction
			ORDER BY sum(wb.notional) DESC
		`, since, out[i].WalletID)
		if err == nil {
			for mr.Next() {
				var m BurstMarketRow
				if err := mr.Scan(&m.MarketID, &m.MarketSlug, &m.MarketTitle, &m.Direction, &m.Notional, &m.BetCount); err == nil {
					out[i].Markets = append(out[i].Markets, m)
				}
			}
			mr.Close()
		}
	}
	return out, nil
}
