// Package marketscan implements the event/market discovery, hotset, and
// holder scan workers. All workers accept ctx and stop gracefully.
package marketscan

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/gamma"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

type DiscoveryWorker struct {
	Gamma            *gamma.Client
	Store            *postgres.Store
	Log              *slog.Logger
	Interval         time.Duration
	TargetCategories []string
	PageLimit        int
}

func (w *DiscoveryWorker) Run(ctx context.Context) error {
	if w.Interval <= 0 {
		w.Interval = 5 * time.Minute
	}
	if w.PageLimit <= 0 {
		w.PageLimit = 100
	}
	// run immediately, then on interval
	w.runOnce(ctx)
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

func (w *DiscoveryWorker) runOnce(ctx context.Context) {
	start := time.Now()
	normalize := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, s := range in {
			x := strings.ToLower(strings.TrimSpace(s))
			if x == "" {
				continue
			}
			if x == "*" || x == "all" {
				return nil
			}
			out = append(out, x)
		}
		return out
	}
	targets := normalize(w.TargetCategories)
	mode := "all"
	if len(targets) > 0 {
		mode = "tags"
	}
	if w.Log != nil {
		w.Log.Info("discovery cycle started", "mode", mode, "tags", targets)
	}

	var fetched, eventsUp, marketsUp, tokensUp, errs int
	active := true
	closed := false

	processEvents := func(evts []gamma.Event) {
		fetched += len(evts)
		for _, e := range evts {
			rawEvent, _ := json.Marshal(e)
			eventID, err := w.Store.UpsertEvent(ctx, postgres.Event{
				PolymarketEventID: e.ID,
				Slug:              e.Slug,
				Title:             e.Title,
				Category:          e.Category,
				Tags:              tagSlugs(e.Tags),
				Raw:               rawEvent,
				Active:            e.Active,
				Closed:            e.Closed,
			})
			if err != nil {
				errs++
				if w.Log != nil {
					w.Log.Warn("upsert event", "slug", e.Slug, "err", err)
				}
				continue
			}
			eventsUp++
			metrics.Inc("discovery_events_upserted_total")
			for _, m := range e.Markets {
				rawM, _ := json.Marshal(m)
				startDate, _ := time.Parse(time.RFC3339, m.StartDate)
				endDate, _ := time.Parse(time.RFC3339, m.EndDate)
				mid, err := w.Store.UpsertMarket(ctx, postgres.Market{
					ConditionID:         m.ConditionID,
					EventID:             eventID,
					Slug:                m.Slug,
					Question:            m.Question,
					Description:         m.Description,
					ResolutionSource:    m.ResolutionSource,
					Active:              m.Active,
					Closed:              m.Closed,
					Volume:              m.Volume.Float64(),
					Liquidity:           m.Liquidity.Float64(),
					NegRisk:             m.NegRisk,
					UMAResolutionStatus: m.UMAResolutionStatus,
					UMABond:             m.UMABond.Float64(),
					StartDate:           startDate,
					EndDate:             endDate,
					Raw:                 rawM,
				})
				if err != nil {
					errs++
					if w.Log != nil {
						w.Log.Warn("upsert market", "condition", m.ConditionID, "err", err)
					}
					continue
				}
				marketsUp++
				metrics.Inc("discovery_markets_upserted_total")
				for idx, tid := range m.ClobTokenIDs {
					name := ""
					if idx < len(m.Outcomes) {
						name = m.Outcomes[idx]
					}
					if err := w.Store.UpsertMarketToken(ctx, mid, postgres.MarketToken{
						OutcomeIndex: idx,
						OutcomeName:  name,
						ClobTokenID:  tid,
					}); err == nil {
						tokensUp++
					}
				}
			}
		}
	}

	fetchPaged := func(tag string) {
		const maxPages = 100
		for page := 0; page < maxPages; page++ {
			offset := page * w.PageLimit
			evts, raw, err := w.Gamma.ListEvents(ctx, gamma.ListEventsParams{
				Tag:    tag,
				Active: &active,
				Closed: &closed,
				Limit:  w.PageLimit,
				Offset: offset,
			})
			if err != nil {
				errs++
				if w.Log != nil {
					w.Log.Warn("discovery list events failed", "tag", tag, "offset", offset, "err", err)
				}
				metrics.Inc("discovery_errors_total")
				return
			}
			_ = raw
			if len(evts) == 0 {
				return
			}
			processEvents(evts)
			if len(evts) < w.PageLimit {
				return
			}
		}
		if w.Log != nil {
			w.Log.Warn("discovery pagination cap reached", "tag", tag, "max_pages", 100)
		}
	}

	if len(targets) == 0 {
		fetchPaged("")
	} else {
		for _, tag := range targets {
			fetchPaged(tag)
		}
	}
	if w.Log != nil {
		w.Log.Info("discovery cycle completed",
			"events_fetched", fetched,
			"events_upserted", eventsUp,
			"markets_upserted", marketsUp,
			"tokens_upserted", tokensUp,
			"errors", errs,
			"duration", time.Since(start).String())
	}
}

func tagSlugs(tags []gamma.Tag) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if t.Slug != "" {
			out = append(out, t.Slug)
		}
	}
	return out
}
