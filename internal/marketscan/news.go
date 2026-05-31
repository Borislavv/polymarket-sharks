package marketscan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/logfields"
	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/nextjs"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// NewsWorker is feature-flagged. Failures here MUST NOT crash core
// surveillance; we swallow errors with logs and continue.
type NewsWorker struct {
	NextJS   *nextjs.Client
	Store    *postgres.Store
	Router   *alerts.Router
	Log      *slog.Logger
	Interval time.Duration
	Enabled  bool
	Links    alerts.LinkBuilder
}

func (w *NewsWorker) Run(ctx context.Context) error {
	if !w.Enabled {
		<-ctx.Done()
		return ctx.Err()
	}
	if w.Interval <= 0 {
		w.Interval = 5 * time.Minute
	}
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			w.runOnce(ctx)
		}
	}
}

func (w *NewsWorker) runOnce(ctx context.Context) {
	start := time.Now()
	events, err := w.Store.ListActiveEvents(ctx, 50)
	if err != nil {
		return
	}
	var inserted, duplicates, alertsCreated int
	for _, e := range events {
		p, err := w.NextJS.FetchEvent(ctx, e.Slug)
		if err != nil {
			metrics.Inc("news_fetch_errors_total")
			continue
		}
		for _, item := range p.News {
			fp := newsFingerprint(e.Slug, item.Title, item.URL, item.Timestamp.Unix())
			n := postgres.NewsItem{
				EventID:       e.ID,
				EventSlug:     e.Slug,
				Title:         item.Title,
				Summary:       item.Summary,
				SourceURL:     item.URL,
				NewsTimestamp: item.Timestamp,
				Fingerprint:   fp,
			}
			_, ins, err := w.Store.InsertNews(ctx, n)
			if err != nil {
				continue
			}
			if !ins {
				duplicates++
				continue
			}
			inserted++
			metrics.Inc("news_items_total{source=" + safeSource(item.Source) + "}")
			if w.Log != nil {
				w.Log.Info("news item found",
					"event_slug", e.Slug,
					"title", logfields.Title(item.Title),
					"source", item.Source)
			}
			body := alerts.FormatNewsAlert(alerts.NewsAlert{
				EventTitle: e.Title,
				EventSlug:  e.Slug,
				Title:      item.Title,
				Summary:    item.Summary,
				SourceURL:  item.URL,
				Time:       item.Timestamp,
			}, w.Links)
			decision := postgres.AlertDecision{
				AlertType:         alerts.TypeNews,
				EntityType:        "news_item",
				EntityID:          fp,
				Severity:          "INFO",
				ShouldSend:        true,
				UserAlertAllowed:  true,
				AdminAlertAllowed: true,
				DedupKey:          alerts.DedupKey("NEWS", fp),
				FeatureSnapshot:   map[string]any{"event_slug": e.Slug, "title": item.Title},
			}
			out := w.Router.Route(ctx, decision, body, alerts.ChannelNews)
			if out.Err != nil && w.Log != nil {
				w.Log.Warn("news alert", "err", out.Err)
			}
			if out.DecisionNew {
				alertsCreated++
			}
		}
	}
	if w.Log != nil {
		w.Log.Info("news cycle completed",
			"events_scanned", len(events),
			"items_inserted", inserted,
			"duplicates", duplicates,
			"alerts_created", alertsCreated,
			"duration", time.Since(start).String())
	}
}

func safeSource(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func newsFingerprint(slug, title, url string, ts int64) string {
	h := sha256.New()
	h.Write([]byte(slug + "|" + title + "|" + url))
	h.Write([]byte{byte(ts), byte(ts >> 8), byte(ts >> 16), byte(ts >> 24)})
	return hex.EncodeToString(h.Sum(nil))
}
