package postgres

import (
	"context"
	"time"
)

type Market struct {
	ID                  string
	ConditionID         string
	EventID             string // optional
	Slug                string
	Question            string
	Description         string
	ResolutionSource    string
	RulesText           string
	Active              bool
	Closed              bool
	Volume              float64
	Liquidity           float64
	NegRisk             bool
	UMAResolutionStatus string
	UMABond             float64
	StartDate           time.Time
	EndDate             time.Time
	Raw                 []byte
}

type MarketToken struct {
	OutcomeIndex int
	OutcomeName  string
	ClobTokenID  string
}

func (s *Store) UpsertMarket(ctx context.Context, m Market) (string, error) {
	if m.Raw == nil {
		m.Raw = []byte(`{}`)
	}
	umaResolved := m.UMAResolutionStatus == "resolved"
	const q = `
		INSERT INTO markets
		    (condition_id, event_id, slug, question, description, resolution_source,
		     rules_text, active, closed, volume, liquidity,
		     neg_risk, uma_resolution_status, uma_resolved, uma_bond,
		     start_date, end_date, raw, updated_at)
		VALUES
		    ($1, NULLIF($2,'')::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11,
		     $12, NULLIF($13,''), $14, NULLIF($15,0),
		     $16, $17, $18::jsonb, now())
		ON CONFLICT (condition_id) DO UPDATE SET
		    event_id              = COALESCE(EXCLUDED.event_id, markets.event_id),
		    slug                  = EXCLUDED.slug,
		    question              = EXCLUDED.question,
		    description           = EXCLUDED.description,
		    resolution_source     = EXCLUDED.resolution_source,
		    rules_text            = EXCLUDED.rules_text,
		    active                = EXCLUDED.active,
		    closed                = EXCLUDED.closed,
		    volume                = EXCLUDED.volume,
		    liquidity             = EXCLUDED.liquidity,
		    neg_risk              = EXCLUDED.neg_risk,
		    uma_resolution_status = COALESCE(EXCLUDED.uma_resolution_status, markets.uma_resolution_status),
		    uma_resolved          = EXCLUDED.uma_resolved,
		    uma_bond              = COALESCE(EXCLUDED.uma_bond, markets.uma_bond),
		    start_date            = COALESCE(EXCLUDED.start_date, markets.start_date),
		    end_date              = COALESCE(EXCLUDED.end_date, markets.end_date),
		    raw                   = EXCLUDED.raw,
		    updated_at            = now()
		RETURNING id::text`
	var id string
	if err := s.Pool.QueryRow(ctx, q,
		m.ConditionID, m.EventID, m.Slug, m.Question, m.Description,
		nilIfEmpty(m.ResolutionSource), nilIfEmpty(m.RulesText),
		m.Active, m.Closed, m.Volume, m.Liquidity,
		m.NegRisk, m.UMAResolutionStatus, umaResolved, m.UMABond,
		nilIfZeroTime(m.StartDate), nilIfZeroTime(m.EndDate),
		string(m.Raw),
	).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) UpsertMarketToken(ctx context.Context, marketID string, t MarketToken) error {
	const q = `
		INSERT INTO market_tokens (market_id, outcome_index, outcome_name, clob_token_id)
		VALUES ($1::uuid, $2, $3, $4)
		ON CONFLICT (clob_token_id) DO UPDATE SET
		    market_id     = EXCLUDED.market_id,
		    outcome_index = EXCLUDED.outcome_index,
		    outcome_name  = EXCLUDED.outcome_name`
	_, err := s.Pool.Exec(ctx, q, marketID, t.OutcomeIndex, t.OutcomeName, t.ClobTokenID)
	return err
}

func (s *Store) UpsertMarketState(ctx context.Context, marketID string, lastPrice, bestBid, bestAsk, spread float64, lastTradeAt, wsSeenAt time.Time) error {
	const q = `
		INSERT INTO market_state (market_id, last_price, best_bid, best_ask, spread, last_trade_at, ws_seen_at, updated_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (market_id) DO UPDATE SET
		    last_price    = COALESCE(NULLIF(EXCLUDED.last_price, 0),    market_state.last_price),
		    best_bid      = COALESCE(NULLIF(EXCLUDED.best_bid, 0),      market_state.best_bid),
		    best_ask      = COALESCE(NULLIF(EXCLUDED.best_ask, 0),      market_state.best_ask),
		    spread        = COALESCE(NULLIF(EXCLUDED.spread, 0),        market_state.spread),
		    last_trade_at = COALESCE(EXCLUDED.last_trade_at, market_state.last_trade_at),
		    ws_seen_at    = COALESCE(EXCLUDED.ws_seen_at, market_state.ws_seen_at),
		    updated_at    = now()`
	_, err := s.Pool.Exec(ctx, q, marketID, lastPrice, bestBid, bestAsk, spread,
		nilIfZeroTime(lastTradeAt), nilIfZeroTime(wsSeenAt))
	return err
}

func nilIfZeroTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

type MarketSummary struct {
	ID                  string
	ConditionID         string
	EventID             string
	EventSlug           string
	EventTitle          string
	Slug                string
	Question            string
	Description         string
	ResolutionSource    string
	RulesText           string
	Volume              float64
	Liquidity           float64
	NegRisk             bool
	UMAResolutionStatus string
	UpdatedAt           time.Time
}

// ListHotsetCandidates returns markets ranked by volume+liquidity for hotset.
func (s *Store) ListHotsetCandidates(ctx context.Context, limit int) ([]MarketSummary, error) {
	if limit <= 0 {
		limit = 80
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, condition_id, COALESCE(event_id::text,''), COALESCE(slug,''),
		       COALESCE(question,''), COALESCE(description,''),
		       COALESCE(resolution_source,''), COALESCE(rules_text,''),
		       COALESCE(volume,0), COALESCE(liquidity,0),
		       COALESCE(neg_risk,false), COALESCE(uma_resolution_status,''),
		       updated_at
		FROM markets
		WHERE active = true AND closed = false
		ORDER BY (COALESCE(volume,0) + COALESCE(liquidity,0)) DESC NULLS LAST,
		         updated_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MarketSummary
	for rows.Next() {
		var m MarketSummary
		if err := rows.Scan(&m.ID, &m.ConditionID, &m.EventID, &m.Slug, &m.Question,
			&m.Description, &m.ResolutionSource, &m.RulesText, &m.Volume, &m.Liquidity,
			&m.NegRisk, &m.UMAResolutionStatus, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetMarketByConditionID(ctx context.Context, conditionID string) (MarketSummary, error) {
	var m MarketSummary
	err := s.Pool.QueryRow(ctx, `
		SELECT id::text, condition_id, COALESCE(event_id::text,''), COALESCE(slug,''),
		       COALESCE(question,''), COALESCE(description,''),
		       COALESCE(resolution_source,''), COALESCE(rules_text,''),
		       COALESCE(volume,0), COALESCE(liquidity,0),
		       COALESCE(neg_risk,false), COALESCE(uma_resolution_status,''),
		       updated_at
		FROM markets
		WHERE condition_id = $1
	`, conditionID).Scan(&m.ID, &m.ConditionID, &m.EventID, &m.Slug, &m.Question,
		&m.Description, &m.ResolutionSource, &m.RulesText, &m.Volume, &m.Liquidity,
		&m.NegRisk, &m.UMAResolutionStatus, &m.UpdatedAt)
	return m, err
}

// ListActiveMarkets returns all active, non-closed markets (optionally
// bounded by limit). Used by global discovery workers that must inspect the
// full universe instead of the hotset subset.
func (s *Store) ListActiveMarkets(ctx context.Context, limit int) ([]MarketSummary, error) {
	base := `
		SELECT id::text, condition_id, COALESCE(event_id::text,''), COALESCE(slug,''),
		       COALESCE(question,''), COALESCE(description,''),
		       COALESCE(resolution_source,''), COALESCE(rules_text,''),
		       COALESCE(volume,0), COALESCE(liquidity,0),
		       COALESCE(neg_risk,false), COALESCE(uma_resolution_status,''),
		       updated_at
		FROM markets
		WHERE active = true AND closed = false
		ORDER BY updated_at DESC, condition_id ASC
	`
	var (
		rows pgxRows
		err  error
	)
	if limit > 0 {
		rows, err = s.Pool.Query(ctx, base+` LIMIT $1`, limit)
	} else {
		rows, err = s.Pool.Query(ctx, base)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MarketSummary
	for rows.Next() {
		var m MarketSummary
		if err := rows.Scan(&m.ID, &m.ConditionID, &m.EventID, &m.Slug, &m.Question,
			&m.Description, &m.ResolutionSource, &m.RulesText, &m.Volume, &m.Liquidity,
			&m.NegRisk, &m.UMAResolutionStatus, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListActiveMarketsWithEvents returns active markets with enough event context
// for text-based strategy matchers and alert links.
func (s *Store) ListActiveMarketsWithEvents(ctx context.Context, limit int) ([]MarketSummary, error) {
	base := `
		SELECT m.id::text, m.condition_id, COALESCE(m.event_id::text,''),
		       COALESCE(e.slug,''), COALESCE(e.title,''),
		       COALESCE(m.slug,''), COALESCE(m.question,''), COALESCE(m.description,''),
		       COALESCE(m.resolution_source,''), COALESCE(m.rules_text,''),
		       COALESCE(m.volume,0), COALESCE(m.liquidity,0),
		       COALESCE(m.neg_risk,false), COALESCE(m.uma_resolution_status,''),
		       m.updated_at
		FROM markets m
		LEFT JOIN events e ON e.id = m.event_id
		WHERE m.active = true AND m.closed = false
		ORDER BY m.updated_at DESC, m.condition_id ASC
	`
	var (
		rows pgxRows
		err  error
	)
	if limit > 0 {
		rows, err = s.Pool.Query(ctx, base+` LIMIT $1`, limit)
	} else {
		rows, err = s.Pool.Query(ctx, base)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MarketSummary
	for rows.Next() {
		var m MarketSummary
		if err := rows.Scan(&m.ID, &m.ConditionID, &m.EventID, &m.EventSlug, &m.EventTitle,
			&m.Slug, &m.Question, &m.Description, &m.ResolutionSource, &m.RulesText,
			&m.Volume, &m.Liquidity, &m.NegRisk, &m.UMAResolutionStatus, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// pgxRows is the minimal subset we use from pgx.Rows so ListActiveMarkets can
// build a small conditional query without duplicating scan loops.
type pgxRows interface {
	Close()
	Err() error
	Next() bool
	Scan(dest ...any) error
}
