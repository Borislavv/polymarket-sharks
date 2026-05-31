package postgres

import (
	"context"
	"encoding/json"
	"time"
)

type Event struct {
	ID                string
	PolymarketEventID string
	Slug              string
	Title             string
	Category          string
	Tags              []string
	Raw               []byte
	Active            bool
	Closed            bool
}

func (s *Store) UpsertEvent(ctx context.Context, e Event) (string, error) {
	if e.Raw == nil {
		e.Raw = []byte(`{}`)
	}
	tagsJSON, err := json.Marshal(e.Tags)
	if err != nil {
		return "", err
	}
	const q = `
		INSERT INTO events
		    (polymarket_event_id, slug, title, category, tags, raw, active, closed, updated_at)
		VALUES
		    ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8, now())
		ON CONFLICT (slug) DO UPDATE SET
		    polymarket_event_id = COALESCE(EXCLUDED.polymarket_event_id, events.polymarket_event_id),
		    title    = EXCLUDED.title,
		    category = EXCLUDED.category,
		    tags     = EXCLUDED.tags,
		    raw      = EXCLUDED.raw,
		    active   = EXCLUDED.active,
		    closed   = EXCLUDED.closed,
		    updated_at = now()
		RETURNING id::text`
	var id string
	if err := s.Pool.QueryRow(ctx, q,
		nilIfEmpty(e.PolymarketEventID), e.Slug, e.Title, nilIfEmpty(e.Category),
		string(tagsJSON), string(e.Raw), e.Active, e.Closed,
	).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

type ListActiveEventsResult struct {
	ID        string
	Slug      string
	Title     string
	Category  string
	UpdatedAt time.Time
}

func (s *Store) ListActiveEvents(ctx context.Context, limit int) ([]ListActiveEventsResult, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, slug, title, COALESCE(category, ''), updated_at
		FROM events
		WHERE active = true AND closed = false
		ORDER BY updated_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ListActiveEventsResult
	for rows.Next() {
		var r ListActiveEventsResult
		if err := rows.Scan(&r.ID, &r.Slug, &r.Title, &r.Category, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
