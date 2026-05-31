package clob

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSubscribeMessageFormat(t *testing.T) {
	b, err := json.Marshal(SubscribeMessage{Type: "MARKET", AssetsIDs: []string{"t1", "t2"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	// per Polymarket CLOB spec: `assets_ids` (with trailing 's' on assets)
	if !strings.Contains(s, `"assets_ids":["t1","t2"]`) {
		t.Fatalf("expected `assets_ids` array key, got %s", s)
	}
	if !strings.Contains(s, `"type":"MARKET"`) {
		t.Fatalf("expected MARKET type, got %s", s)
	}
}

func TestUnsubscribeMessageFormat(t *testing.T) {
	b, _ := json.Marshal(UnsubscribeMessage{Type: "MARKET_UNSUBSCRIBE", AssetsIDs: []string{"a"}})
	if !strings.Contains(string(b), `"type":"MARKET_UNSUBSCRIBE"`) {
		t.Fatalf("got %s", b)
	}
}

func TestSubscribe_QueuedBeforeConnect(t *testing.T) {
	c := NewWS("ws://invalid", nil, time.Second)
	if err := c.Subscribe(context.Background(), "t1", "t2"); err != nil {
		t.Fatalf("subscribe queue: %v", err)
	}
	got := c.SubscribedTokens()
	if len(got) != 2 {
		t.Fatalf("expected 2 queued, got %v", got)
	}
}

func TestRoundtrip_SubscribeAndReceive(t *testing.T) {
	var receivedSub atomicBool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// expect subscribe message first
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg SubscribeMessage
		if err := json.Unmarshal(data, &msg); err == nil && msg.Type == "MARKET" {
			receivedSub.set(true)
		}
		// send a market event
		ev := WSEvent{EventType: "book", AssetID: "t1", Market: "0xM"}
		b, _ := json.Marshal(ev)
		_ = conn.Write(ctx, websocket.MessageText, b)
		time.Sleep(100 * time.Millisecond)
		conn.Close(websocket.StatusNormalClosure, "done")
	}))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	cli := NewWS(url, nil, 100*time.Millisecond)
	gotCh := make(chan WSEvent, 1)
	cli.OnEvent(func(e WSEvent) { gotCh <- e })
	_ = cli.Subscribe(context.Background(), "t1") // queued for replay
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go cli.Run(ctx)
	select {
	case ev := <-gotCh:
		if ev.AssetID != "t1" {
			t.Fatalf("unexpected event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("did not receive event")
	}
	if !receivedSub.get() {
		t.Fatalf("server did not see subscribe message")
	}
}

type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (a *atomicBool) set(b bool) { a.mu.Lock(); a.v = b; a.mu.Unlock() }
func (a *atomicBool) get() bool  { a.mu.Lock(); defer a.mu.Unlock(); return a.v }
