package marketscan

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/clob"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// PriceSamplerWorker periodically samples CLOB midpoint/book for hotset
// market tokens and persists them to market_price_samples. Output feeds
// the shark_score CLV proxy.
//
// Bounded: limits the number of tokens sampled per cycle so we never
// exhaust the CLOB rate limit. Failures per token are logged + counted.
type PriceSamplerWorker struct {
	CLOB        *clob.RESTClient
	Store       *postgres.Store
	Log         *slog.Logger
	Interval    time.Duration
	MaxPerCycle int
}

type tokenRow struct {
	MarketID string
	TokenID  string
	ClobID   string
	Outcome  string
}

func (w *PriceSamplerWorker) Run(ctx context.Context) error {
	if w.Interval <= 0 {
		w.Interval = 2 * time.Minute
	}
	if w.MaxPerCycle <= 0 {
		w.MaxPerCycle = 40
	}
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	w.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			w.runOnce(ctx)
		}
	}
}

func (w *PriceSamplerWorker) runOnce(ctx context.Context) {
	rows, err := w.Store.Pool.Query(ctx, `
		SELECT m.id::text, mt.id::text, mt.clob_token_id, COALESCE(mt.outcome_name,'')
		FROM market_tokens mt
		JOIN markets m ON m.id = mt.market_id
		WHERE m.active = true AND m.closed = false
		ORDER BY (COALESCE(m.volume,0) + COALESCE(m.liquidity,0)) DESC NULLS LAST
		LIMIT $1
	`, w.MaxPerCycle*2) // tokens > markets (2 outcomes typical)
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("price sampler list tokens", "err", err)
		}
		return
	}
	defer rows.Close()
	var tokens []tokenRow
	for rows.Next() {
		var tr tokenRow
		if err := rows.Scan(&tr.MarketID, &tr.TokenID, &tr.ClobID, &tr.Outcome); err == nil {
			tokens = append(tokens, tr)
		}
	}
	for i, tr := range tokens {
		if i >= w.MaxPerCycle {
			break
		}
		if ctx.Err() != nil {
			return
		}
		book, _, errB := w.CLOB.GetBook(ctx, tr.ClobID)
		var bestBid, bestAsk, mid float64
		if errB == nil && book != nil {
			bestBid = book.BestBid()
			bestAsk = book.BestAsk()
			if bestBid > 0 && bestAsk > 0 {
				mid = (bestBid + bestAsk) / 2
			}
		} else if w.Log != nil {
			w.Log.Debug("clob book unavailable", "token", tr.ClobID, "err", errB)
		}
		if mid == 0 {
			m, _, err := w.CLOB.GetMidpoint(ctx, tr.ClobID)
			if err == nil {
				mid = m
			}
		}
		if mid == 0 && bestBid == 0 && bestAsk == 0 {
			metrics.Inc("price_sampler_misses_total")
			continue
		}
		raw, _ := json.Marshal(map[string]any{"best_bid": bestBid, "best_ask": bestAsk, "mid": mid})
		err := w.Store.InsertPriceSample(ctx, postgres.PriceSample{
			MarketID:  tr.MarketID,
			TokenID:   tr.TokenID,
			Outcome:   tr.Outcome,
			Price:     mid,
			Midpoint:  mid,
			BestBid:   bestBid,
			BestAsk:   bestAsk,
			SampledAt: time.Now(),
			Source:    "clob_rest",
			Raw:       raw,
		})
		if err != nil {
			metrics.Inc("price_sampler_errors_total")
			if w.Log != nil {
				w.Log.Warn("insert price sample", "err", err)
			}
		} else {
			metrics.Inc("price_sampler_samples_total")
			// Also refresh market_state best_bid/best_ask/last_price/spread.
			spread := 0.0
			if bestBid > 0 && bestAsk > 0 {
				spread = bestAsk - bestBid
			}
			_ = w.Store.UpsertMarketState(ctx, tr.MarketID, mid, bestBid, bestAsk, spread, time.Time{}, time.Now())
		}
	}
}
