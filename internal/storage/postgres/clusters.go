package postgres

import (
	"context"
	"encoding/json"
	"time"
)

// ClusterRow is the storage-level record. Workers convert from
// walletintel.ClusterResult to this struct so postgres does not need to
// import walletintel (avoids cycle).
type ClusterRow struct {
	MarketID         string
	EventID          string
	Direction        string
	DirectionOutcome string // non-empty for categorical (OUTCOME_BUY/SELL) clusters
	WindowStart      time.Time
	WindowEnd        time.Time
	WalletCount      int
	TotalNotional    float64
	WeightedPrice    float64
	AverageOdds      float64
	PayoffIfWinTotal float64
	ClusterScore     int
	WatchedBetIDs    []string
	WalletIDs        []string
	ReasonCodes      []string
	FeatureSnapshot  map[string]any
	DedupKey         string
}

// UpsertCluster inserts or refreshes by dedup_key. Returns (id, inserted_new).
func (s *Store) UpsertCluster(ctx context.Context, c ClusterRow) (string, bool, error) {
	snap, err := json.Marshal(c.FeatureSnapshot)
	if err != nil {
		return "", false, err
	}
	betIDs, _ := json.Marshal(c.WatchedBetIDs)
	walletIDs, _ := json.Marshal(c.WalletIDs)

	const q = `
		INSERT INTO bet_clusters
		    (market_id, event_id, direction, direction_outcome, window_start, window_end,
		     wallet_count, total_notional, weighted_price, average_odds, payoff_if_win_total,
		     cluster_score, watched_bet_ids, wallet_ids, reason_codes,
		     feature_snapshot, dedup_key, updated_at)
		VALUES
		    (NULLIF($1,'')::uuid, NULLIF($2,'')::uuid, $3, NULLIF($4,''), $5, $6,
		     $7, $8, $9, $10, $11, $12, $13::jsonb, $14::jsonb, $15,
		     $16::jsonb, $17, now())
		ON CONFLICT (dedup_key) DO UPDATE SET
		    wallet_count        = GREATEST(EXCLUDED.wallet_count, bet_clusters.wallet_count),
		    total_notional      = GREATEST(EXCLUDED.total_notional, bet_clusters.total_notional),
		    weighted_price      = EXCLUDED.weighted_price,
		    average_odds        = EXCLUDED.average_odds,
		    payoff_if_win_total = EXCLUDED.payoff_if_win_total,
		    cluster_score       = GREATEST(EXCLUDED.cluster_score, bet_clusters.cluster_score),
		    watched_bet_ids     = EXCLUDED.watched_bet_ids,
		    wallet_ids          = EXCLUDED.wallet_ids,
		    reason_codes        = EXCLUDED.reason_codes,
		    feature_snapshot    = EXCLUDED.feature_snapshot,
		    updated_at          = now()
		RETURNING id::text, (xmax = 0) AS inserted`
	var id string
	var inserted bool
	err = s.Pool.QueryRow(ctx, q,
		c.MarketID, c.EventID, c.Direction, c.DirectionOutcome, c.WindowStart, c.WindowEnd,
		c.WalletCount, c.TotalNotional, c.WeightedPrice, c.AverageOdds, c.PayoffIfWinTotal,
		c.ClusterScore, string(betIDs), string(walletIDs),
		reasonsArr(c.ReasonCodes), string(snap), c.DedupKey,
	).Scan(&id, &inserted)
	if err != nil {
		return "", false, err
	}
	return id, inserted, nil
}
