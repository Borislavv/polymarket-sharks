package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type NewsItem struct {
	EventID       string
	EventSlug     string
	Title         string
	Summary       string
	SourceURL     string
	NewsTimestamp time.Time
	Raw           []byte
	Fingerprint   string
}

// InsertNews is idempotent by fingerprint.
// Returns (id, inserted_new).
func (s *Store) InsertNews(ctx context.Context, n NewsItem) (string, bool, error) {
	if n.Raw == nil {
		n.Raw = []byte(`{}`)
	}
	const q = `
		INSERT INTO news_items
		    (event_id, event_slug, title, summary, source_url, news_timestamp, raw, fingerprint)
		VALUES
		    (NULLIF($1,'')::uuid, NULLIF($2,''), $3, NULLIF($4,''), NULLIF($5,''), $6, $7::jsonb, $8)
		ON CONFLICT (fingerprint) DO NOTHING
		RETURNING id::text`
	var id string
	err := s.Pool.QueryRow(ctx, q,
		n.EventID, n.EventSlug, n.Title, n.Summary, n.SourceURL,
		nilIfZeroTime(n.NewsTimestamp), string(n.Raw), n.Fingerprint,
	).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err2 := s.Pool.QueryRow(ctx, `SELECT id::text FROM news_items WHERE fingerprint=$1`, n.Fingerprint).Scan(&id)
		if err2 != nil {
			return "", false, err2
		}
		return id, false, nil
	}
	return "", false, err
}
