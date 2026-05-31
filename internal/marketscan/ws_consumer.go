package marketscan

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/clob"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// TokenCache resolves clob_token_id → (market_id, outcome_name, token_id).
// Loaded lazily on cache miss; entries never expire (token mappings are
// immutable per market). HotsetWorker can call Invalidate when discovery
// adds new tokens to force a re-read.
type TokenCache struct {
	store *postgres.Store
	mu    sync.RWMutex
	m     map[string]TokenInfo
}

type TokenInfo struct {
	MarketID    string
	TokenID     string
	OutcomeName string
}

func NewTokenCache(store *postgres.Store) *TokenCache {
	return &TokenCache{store: store, m: map[string]TokenInfo{}}
}

func (c *TokenCache) Lookup(ctx context.Context, clobTokenID string) (TokenInfo, bool) {
	c.mu.RLock()
	if v, ok := c.m[clobTokenID]; ok {
		c.mu.RUnlock()
		return v, true
	}
	c.mu.RUnlock()
	var info TokenInfo
	err := c.store.Pool.QueryRow(ctx, `
		SELECT market_id::text, id::text, COALESCE(outcome_name,'')
		FROM market_tokens WHERE clob_token_id = $1
	`, clobTokenID).Scan(&info.MarketID, &info.TokenID, &info.OutcomeName)
	if err != nil {
		return info, false
	}
	c.mu.Lock()
	c.m[clobTokenID] = info
	c.mu.Unlock()
	return info, true
}

func (c *TokenCache) Invalidate() {
	c.mu.Lock()
	c.m = map[string]TokenInfo{}
	c.mu.Unlock()
}

// WSConsumer consumes CLOB WS events and updates market_state +
// market_price_samples for known tokens. UNKNOWN asset ids are counted
// as a diagnostic but never panic. WS is NEVER used to infer wallet
// identity / side / direction — direction is Data API truth.
type WSConsumer struct {
	Store *postgres.Store
	Cache *TokenCache
	Log   *slog.Logger
}

// HandleEvent is the callback wired into WSClient.OnEvent. Synchronous
// (the WS goroutine calls it); kept fast — single DB upsert per message.
func (c *WSConsumer) HandleEvent(ev clob.WSEvent) {
	if c.Store == nil || c.Cache == nil {
		return
	}
	if ev.AssetID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, ok := c.Cache.Lookup(ctx, ev.AssetID)
	if !ok {
		metrics.Inc("ws_unknown_asset_total")
		return
	}
	bestBid, bestAsk, lastPrice := parseWSPrices(ev.Raw)
	if bestBid == 0 && bestAsk == 0 && lastPrice == 0 {
		// nothing actionable in this event (e.g. book delta we ignore)
		return
	}
	mid := 0.0
	if bestBid > 0 && bestAsk > 0 {
		mid = (bestBid + bestAsk) / 2
	}
	spread := 0.0
	if bestBid > 0 && bestAsk > 0 {
		spread = bestAsk - bestBid
	}
	priceForSnapshot := lastPrice
	if priceForSnapshot == 0 {
		priceForSnapshot = mid
	}
	if err := c.Store.UpsertMarketState(ctx, info.MarketID,
		priceForSnapshot, bestBid, bestAsk, spread,
		time.Time{}, time.Now()); err != nil {
		metrics.Inc("ws_market_state_errors_total")
		if c.Log != nil {
			c.Log.Warn("ws market_state upsert", "err", err)
		}
		return
	}
	metrics.Inc("ws_market_state_updates_total")
	if err := c.Store.InsertPriceSample(ctx, postgres.PriceSample{
		MarketID:  info.MarketID,
		TokenID:   info.TokenID,
		Outcome:   info.OutcomeName,
		Price:     priceForSnapshot,
		Midpoint:  mid,
		BestBid:   bestBid,
		BestAsk:   bestAsk,
		SampledAt: time.Now(),
		Source:    "ws",
		Raw:       ev.Raw,
	}); err == nil {
		metrics.Inc("ws_price_samples_total")
	}
}

// parseWSPrices defensively extracts best_bid/best_ask/last_trade_price
// from a CLOB WS message. Polymarket emits several event types ("book",
// "price_change", "last_trade_price", "tick_size_change") with overlapping
// fields; we walk a small set of known shapes and stop at the first hit.
//
// All numeric fields may be strings or numbers in Polymarket payloads.
func parseWSPrices(raw json.RawMessage) (bestBid, bestAsk, lastTrade float64) {
	if len(raw) == 0 {
		return 0, 0, 0
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, 0, 0
	}
	// direct best bid / best ask fields
	bestBid = anyFloat(m["best_bid"])
	bestAsk = anyFloat(m["best_ask"])
	if bb := anyFloat(m["bestBid"]); bestBid == 0 && bb > 0 {
		bestBid = bb
	}
	if ba := anyFloat(m["bestAsk"]); bestAsk == 0 && ba > 0 {
		bestAsk = ba
	}
	// price_change events carry `price` + `side`
	if p := anyFloat(m["price"]); p > 0 {
		side, _ := m["side"].(string)
		switch side {
		case "BUY", "buy":
			if bestBid == 0 || p > bestBid {
				bestBid = p
			}
		case "SELL", "sell":
			if bestAsk == 0 || p < bestAsk {
				bestAsk = p
			}
		}
	}
	// last_trade_price events
	if p := anyFloat(m["last_trade_price"]); p > 0 {
		lastTrade = p
	}
	if p := anyFloat(m["lastTradePrice"]); lastTrade == 0 && p > 0 {
		lastTrade = p
	}
	// book snapshot: top-of-book bid is highest bid level; top ask is lowest ask
	if bids, ok := m["bids"].([]any); ok {
		bestBid = bestOfBookSide(bids, true, bestBid)
	}
	if asks, ok := m["asks"].([]any); ok {
		bestAsk = bestOfBookSide(asks, false, bestAsk)
	}
	return bestBid, bestAsk, lastTrade
}

func bestOfBookSide(levels []any, takeMax bool, current float64) float64 {
	best := current
	for _, l := range levels {
		obj, ok := l.(map[string]any)
		if !ok {
			continue
		}
		p := anyFloat(obj["price"])
		if p <= 0 || p > 1 {
			continue
		}
		if best == 0 {
			best = p
			continue
		}
		if takeMax && p > best {
			best = p
		} else if !takeMax && p < best {
			best = p
		}
	}
	return best
}

func anyFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		if x == "" {
			return 0
		}
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0
		}
		return f
	}
	return 0
}
