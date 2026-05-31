package clob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/coder/websocket"
)

// WSEvent is the envelope our consumer sees. CLOB WS messages carry
// `event_type`, `market` (condition id), `asset_id` (token id), `price`,
// etc. We do NOT use this stream for wallet identity/side/direction —
// direction comes from Data API `/trades` (truth source).
type WSEvent struct {
	EventType string          `json:"event_type"`
	AssetID   string          `json:"asset_id"`
	Market    string          `json:"market"`
	Raw       json.RawMessage `json:"-"`
}

// SubscribeMessage is the canonical subscribe payload for the CLOB
// market WS. Field is `assets_ids` (note the 's' on assets) — this is the
// documented Polymarket spec; field-name mismatch silently no-ops.
type SubscribeMessage struct {
	Type      string   `json:"type"` // "MARKET"
	AssetsIDs []string `json:"assets_ids"`
}

type UnsubscribeMessage struct {
	Type      string   `json:"type"` // "MARKET_UNSUBSCRIBE"
	AssetsIDs []string `json:"assets_ids"`
}

// WSClient maintains hotset subscriptions.
type WSClient struct {
	URL     string
	Log     *slog.Logger
	HBEvery time.Duration

	mu         sync.Mutex
	conn       *websocket.Conn
	subscribed map[string]struct{}
	onEvent    func(WSEvent)
}

func NewWS(url string, log *slog.Logger, hb time.Duration) *WSClient {
	if hb <= 0 {
		hb = 10 * time.Second
	}
	return &WSClient{
		URL:        url,
		Log:        log,
		HBEvery:    hb,
		subscribed: map[string]struct{}{},
	}
}

func (c *WSClient) OnEvent(fn func(WSEvent)) {
	c.mu.Lock()
	c.onEvent = fn
	c.mu.Unlock()
}

func (c *WSClient) Run(ctx context.Context) error {
	// Periodic summary every 45s so we see liveness without per-event spam.
	go c.summaryLoop(ctx, 45*time.Second)
	backoff := time.Second
	reconnects := 0
	for {
		if err := c.runOnce(ctx); err != nil {
			if c.Log != nil {
				c.Log.Warn("clob ws disconnected", "err", err, "backoff", backoff.String())
			}
			metrics.Inc("ws_reconnects_total")
			reconnects++
			if !sleep(ctx, backoff) {
				return ctx.Err()
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			if c.Log != nil {
				c.Log.Info("clob ws reconnected", "attempt", reconnects)
			}
			continue
		}
		backoff = time.Second
	}
}

// summaryLoop emits a `clob ws summary` line every `every` seconds with
// current counter snapshots. Cheap (atomic loads) and hardcoded — no env.
func (c *WSClient) summaryLoop(ctx context.Context, every time.Duration) {
	if c.Log == nil {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Log.Info("clob ws summary",
				"events_received", metrics.CounterValue("ws_messages_total"),
				"market_state_updates", metrics.CounterValue("ws_market_state_updates_total"),
				"price_samples", metrics.CounterValue("ws_price_samples_total"),
				"unknown_assets", metrics.CounterValue("ws_unknown_asset_total"))
		}
	}
}

func (c *WSClient) runOnce(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, c.URL, nil)
	if err != nil {
		return err
	}
	// CLOB WS pushes book snapshots that frequently exceed 1MB. Default
	// coder/websocket read limit is 32KB → bump to 4MB.
	conn.SetReadLimit(4 << 20)
	defer conn.CloseNow()
	c.mu.Lock()
	c.conn = conn
	subs := make([]string, 0, len(c.subscribed))
	for k := range c.subscribed {
		subs = append(subs, k)
	}
	c.mu.Unlock()
	if c.Log != nil {
		c.Log.Info("clob ws connected", "url", c.URL, "replay_subs", len(subs))
	}

	if len(subs) > 0 {
		if err := c.sendSubscribeRaw(ctx, conn, subs); err != nil {
			return err
		}
	}

	hbCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go c.heartbeat(hbCtx)

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var ev WSEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		ev.Raw = data
		metrics.Inc("ws_messages_total")
		c.mu.Lock()
		cb := c.onEvent
		c.mu.Unlock()
		if cb != nil {
			cb(ev)
		}
	}
}

func (c *WSClient) heartbeat(ctx context.Context) {
	t := time.NewTicker(c.HBEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()
			if conn == nil {
				return
			}
			if err := conn.Ping(ctx); err != nil {
				return
			}
		}
	}
}

// Subscribe queues + sends a MARKET subscribe.
func (c *WSClient) Subscribe(ctx context.Context, tokenIDs ...string) error {
	c.mu.Lock()
	for _, id := range tokenIDs {
		c.subscribed[id] = struct{}{}
	}
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil // queued; will fire on next connect
	}
	return c.sendSubscribeRaw(ctx, conn, tokenIDs)
}

// Unsubscribe sends MARKET_UNSUBSCRIBE.
func (c *WSClient) Unsubscribe(ctx context.Context, tokenIDs ...string) error {
	c.mu.Lock()
	for _, id := range tokenIDs {
		delete(c.subscribed, id)
	}
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil
	}
	b, _ := json.Marshal(UnsubscribeMessage{Type: "MARKET_UNSUBSCRIBE", AssetsIDs: tokenIDs})
	return conn.Write(ctx, websocket.MessageText, b)
}

func (c *WSClient) sendSubscribeRaw(ctx context.Context, conn *websocket.Conn, tokenIDs []string) error {
	if conn == nil {
		return fmt.Errorf("clob ws: not connected")
	}
	b, _ := json.Marshal(SubscribeMessage{Type: "MARKET", AssetsIDs: tokenIDs})
	return conn.Write(ctx, websocket.MessageText, b)
}

// SubscribedTokens returns a snapshot of currently subscribed token ids
// (used by metrics + readiness diagnostics).
func (c *WSClient) SubscribedTokens() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.subscribed))
	for k := range c.subscribed {
		out = append(out, k)
	}
	return out
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
