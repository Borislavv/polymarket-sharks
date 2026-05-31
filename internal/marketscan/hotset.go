package marketscan

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/logfields"
	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/clob"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// HotsetWorker maintains a bounded set of "hot" markets and keeps the
// CLOB WS subscriptions aligned with that set. Recomputes on interval.
type HotsetWorker struct {
	Store      *postgres.Store
	WS         *clob.WSClient
	Log        *slog.Logger
	Interval   time.Duration
	MaxMarkets int

	mu      sync.Mutex
	current map[string]struct{} // active clob token ids
}

func (w *HotsetWorker) Run(ctx context.Context) error {
	if w.Interval <= 0 {
		w.Interval = 90 * time.Second
	}
	if w.MaxMarkets <= 0 {
		w.MaxMarkets = 80
	}
	if w.current == nil {
		w.current = map[string]struct{}{}
	}
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

// tokenMeta resolves clob_token_id → (market_id, slug, title, outcome).
type tokenMeta struct {
	MarketID    string
	Slug        string
	Title       string
	OutcomeName string
}

func (w *HotsetWorker) runOnce(ctx context.Context) {
	start := time.Now()

	// Expose configured limit as a gauge so operators can verify the target.
	metrics.SetGauge("hotset_limit", int64(w.MaxMarkets))

	// Count all active/open markets in DB (bounded, cheap COUNT(*)).
	var activeOpenCount int64
	if err := w.Store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM markets WHERE active=true AND coalesce(closed,false)=false`,
	).Scan(&activeOpenCount); err == nil {
		metrics.SetGauge("hotset_active_open_seen", activeOpenCount)
	}

	mkts, err := w.Store.ListHotsetCandidates(ctx, w.MaxMarkets)
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("hotset list", "err", err)
		}
		return
	}
	// gather all clobTokenIds + per-token metadata for these markets
	target := map[string]struct{}{}
	tokenInfo := map[string]tokenMeta{}
	// market_id → (slug,title) for grouped log
	marketMeta := map[string]struct{ Slug, Title string }{}
	for _, m := range mkts {
		marketMeta[m.ID] = struct{ Slug, Title string }{m.Slug, m.Question}
		rows, err := w.Store.Pool.Query(ctx,
			`SELECT clob_token_id, COALESCE(outcome_name,'') FROM market_tokens WHERE market_id=$1::uuid`,
			m.ID)
		if err != nil {
			continue
		}
		for rows.Next() {
			var tid, name string
			if err := rows.Scan(&tid, &name); err == nil && tid != "" {
				target[tid] = struct{}{}
				tokenInfo[tid] = tokenMeta{MarketID: m.ID, Slug: m.Slug, Title: m.Question, OutcomeName: name}
			}
		}
		rows.Close()
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	var toSub, toUnsub []string
	for tid := range target {
		if _, ok := w.current[tid]; !ok {
			toSub = append(toSub, tid)
		}
	}
	for tid := range w.current {
		if _, ok := target[tid]; !ok {
			toUnsub = append(toUnsub, tid)
		}
	}
	if w.WS != nil {
		if len(toSub) > 0 {
			_ = w.WS.Subscribe(ctx, toSub...)
			metrics.Add("hotset_subscribes_total", int64(len(toSub)))
			w.logSubscribes(toSub, tokenInfo)
		}
		if len(toUnsub) > 0 {
			_ = w.WS.Unsubscribe(ctx, toUnsub...)
			metrics.Add("hotset_unsubscribes_total", int64(len(toUnsub)))
			w.logUnsubscribes(toUnsub)
		}
	}
	w.current = target
	metrics.SetGauge("hotset_size", int64(len(target)))
	if w.Log != nil {
		w.Log.Info("hotset cycle completed",
			"selected", len(mkts),
			"subscribed", len(toSub),
			"unsubscribed", len(toUnsub),
			"unchanged", len(target)-len(toSub),
			"duration", time.Since(start).String())
	}
}

// logSubscribes emits one "market subscribed" line per market (not per token).
// Both YES + NO tokens for the same market are collapsed for readability.
func (w *HotsetWorker) logSubscribes(tokenIDs []string, info map[string]tokenMeta) {
	if w.Log == nil {
		return
	}
	type group struct {
		Slug, Title, YesShort, NoShort string
	}
	groups := map[string]*group{}
	for _, tid := range tokenIDs {
		meta, ok := info[tid]
		if !ok {
			continue
		}
		g := groups[meta.MarketID]
		if g == nil {
			g = &group{Slug: meta.Slug, Title: logfields.Title(meta.Title)}
			groups[meta.MarketID] = g
		}
		switch meta.OutcomeName {
		case "Yes", "YES":
			g.YesShort = logfields.Short(tid)
		case "No", "NO":
			g.NoShort = logfields.Short(tid)
		default:
			if g.YesShort == "" {
				g.YesShort = logfields.Short(tid)
			} else {
				g.NoShort = logfields.Short(tid)
			}
		}
	}
	for mid, g := range groups {
		w.Log.Info("market subscribed",
			"market_id", mid,
			"slug", g.Slug,
			"title", g.Title,
			"yes_token_short", g.YesShort,
			"no_token_short", g.NoShort)
	}
}

func (w *HotsetWorker) logUnsubscribes(tokenIDs []string) {
	if w.Log == nil {
		return
	}
	// We deliberately log only count + token shorts; market resolution would
	// require a fresh DB lookup against potentially-deleted rows.
	for _, tid := range tokenIDs {
		w.Log.Info("market unsubscribed",
			"token_short", logfields.Short(tid),
			"reason", "out_of_hotset")
	}
}
