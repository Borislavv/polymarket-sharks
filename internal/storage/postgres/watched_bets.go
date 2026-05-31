package postgres

import (
	"context"
	"encoding/json"
	"time"
)

type WatchedBet struct {
	ID               string
	WalletTradeID    string
	WalletID         string
	MarketID         string
	Direction        string
	Outcome          string // raw outcome label (YES / NO / categorical name)
	DirectionOutcome string // non-empty only for categorical (OUTCOME_BUY/SELL) bets
	Side             string // BUY | SELL
	Notional         float64
	Price            float64
	Odds             float64
	PayoffIfWin      float64
	WalletClass      string
	WalletScore      int
	ReasonCodes      []string
	FeatureSnapshot  map[string]any
	DetectedAt       time.Time
	BetKind          string // "entry" or "exit"; default "entry"
}

func (s *Store) InsertWatchedBet(ctx context.Context, b WatchedBet) (string, error) {
	snap, err := json.Marshal(b.FeatureSnapshot)
	if err != nil {
		return "", err
	}
	kind := b.BetKind
	if kind == "" {
		kind = "entry"
	}
	const q = `
		INSERT INTO watched_bets
		    (wallet_trade_id, wallet_id, market_id, direction, outcome, direction_outcome, side,
		     notional, price, odds, payoff_if_win, wallet_class, wallet_score,
		     reason_codes, feature_snapshot, detected_at, bet_kind)
		VALUES
		    ($1::uuid, $2::uuid, NULLIF($3,'')::uuid, $4, $5, $6, $7, $8, $9, $10, $11,
		     $12, $13, $14, $15::jsonb, $16, $17)
		RETURNING id::text`
	var id string
	err = s.Pool.QueryRow(ctx, q,
		b.WalletTradeID, b.WalletID, b.MarketID,
		b.Direction, nullIfEmpty(b.Outcome), nullIfEmpty(b.DirectionOutcome), nullIfEmpty(b.Side),
		b.Notional, b.Price, b.Odds, b.PayoffIfWin,
		b.WalletClass, b.WalletScore,
		reasonsArr(b.ReasonCodes), string(snap), b.DetectedAt, kind,
	).Scan(&id)
	return id, err
}

type RecentWatchedBet struct {
	ID               string
	WalletID         string
	WalletClass      string
	WalletScore      int
	MarketID         string
	MarketSlug       string
	MarketTitle      string
	EventID          string
	EventSlug        string
	EventTitle       string
	Direction        string
	DirectionOutcome string // non-empty only for categorical bets
	Notional         float64
	Price            float64
	DetectedAt       time.Time
}

// ListRecentWatchedBets returns watched bets in the last window for
// many-trader cluster detection. By default returns only `entry` bets so
// EXIT_CLUSTER_ENABLED actually means something (callers pass
// includeExits=true to opt in).
func (s *Store) ListRecentWatchedBets(ctx context.Context, since time.Time, includeExits bool) ([]RecentWatchedBet, error) {
	q := `
		SELECT wb.id::text, wb.wallet_id::text, wb.wallet_class, wb.wallet_score,
		       COALESCE(wb.market_id::text,''),
		       COALESCE(m.slug,''), COALESCE(m.question,''),
		       COALESCE(m.event_id::text,''),
		       COALESCE(e.slug,''), COALESCE(e.title,''),
		       wb.direction, COALESCE(wb.direction_outcome,''),
		       COALESCE(wb.notional,0), COALESCE(wb.price,0), wb.detected_at
		FROM watched_bets wb
		LEFT JOIN markets m ON m.id = wb.market_id
		LEFT JOIN events  e ON e.id = m.event_id
		WHERE wb.detected_at >= $1
	`
	if !includeExits {
		q += ` AND wb.bet_kind = 'entry'`
	}
	q += ` ORDER BY wb.detected_at ASC`
	rows, err := s.Pool.Query(ctx, q, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecentWatchedBet
	for rows.Next() {
		var r RecentWatchedBet
		if err := rows.Scan(&r.ID, &r.WalletID, &r.WalletClass, &r.WalletScore,
			&r.MarketID, &r.MarketSlug, &r.MarketTitle,
			&r.EventID, &r.EventSlug, &r.EventTitle,
			&r.Direction, &r.DirectionOutcome,
			&r.Notional, &r.Price, &r.DetectedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// nullIfEmpty converts an empty string to nil so the DB receives NULL.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
