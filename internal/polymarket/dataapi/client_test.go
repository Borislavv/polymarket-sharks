package dataapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
)

func newClient(base string) *Client {
	return New(polymarket.New(base, 50, 2*time.Second))
}

func TestGetHoldersByMarket_RealShape(t *testing.T) {
	// fixture captured from real /holders on 2026-05-24
	raw, err := os.ReadFile("../testdata/dataapi_holders.json")
	if err != nil {
		t.Fatalf("missing fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/holders" {
			t.Fatalf("unexpected path: %v", r.URL.Path)
		}
		w.Write(raw)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	out, _, err := c.GetHoldersByMarket(context.Background(), "0xCID")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("expected flattened holders, got 0")
	}
	for _, h := range out {
		if h.ProxyWallet == "" || h.Token == "" {
			t.Fatalf("missing fields in flat holder: %+v", h)
		}
		if h.Amount <= 0 {
			t.Fatalf("expected positive amount, got %v", h.Amount)
		}
	}
}

func TestGetTrades_RealShape(t *testing.T) {
	raw, err := os.ReadFile("../testdata/dataapi_trades.json")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(raw)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	out, _, err := c.GetTrades(context.Background(), "", "0xCID", false, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("expected trades")
	}
	if out[0].Side != "BUY" && out[0].Side != "SELL" {
		t.Fatalf("expected BUY/SELL got %q", out[0].Side)
	}
	if out[0].Outcome != "Yes" && out[0].Outcome != "No" {
		t.Fatalf("expected Yes/No got %q", out[0].Outcome)
	}
	if out[0].Price.Float64() <= 0 || out[0].Price.Float64() > 1 {
		t.Fatalf("price out of range: %v", out[0].Price.Float64())
	}
	if out[0].Timestamp.Int64() == 0 {
		t.Fatalf("missing timestamp")
	}
}

func TestGetActivity_RealShape(t *testing.T) {
	raw, err := os.ReadFile("../testdata/dataapi_activity.json")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(raw)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	out, _, err := c.GetActivity(context.Background(), "0xabc", 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("expected at least one activity")
	}
	if out[0].TransactionHash == "" {
		t.Fatalf("missing transaction hash")
	}
}

func TestGetUserPositions_RealShape(t *testing.T) {
	raw, err := os.ReadFile("../testdata/dataapi_positions.json")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(raw)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	out, _, err := c.GetUserPositions(context.Background(), "0xabc")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("expected positions")
	}
	// realizedPnl should parse — value or zero
	_ = out[0].RealizedPnl.Float64()
	if out[0].ConditionID == "" {
		t.Fatalf("missing conditionId")
	}
}

func TestGetTradedCount_RealShape(t *testing.T) {
	raw, err := os.ReadFile("../testdata/dataapi_traded.json")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(raw)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	n, _, err := c.GetTradedCount(context.Background(), "0xabc")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected traded count > 0, got %d", n)
	}
}

func TestParse_TolerantToExtraFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"token":"t1","holders":[{"proxyWallet":"0xa","amount":"42.5","outcomeIndex":0,"extra_unknown_field":123}]}]`))
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	out, _, err := c.GetHoldersByMarket(context.Background(), "x")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 1 || out[0].Amount != 42.5 {
		t.Fatalf("expected amount 42.5 parsed from string, got %+v", out)
	}
}

func TestParse_NullFieldsOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"token":"t1","holders":[{"proxyWallet":"0xa","amount":null,"pseudonym":null,"outcomeIndex":0}]}]`))
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	out, _, err := c.GetHoldersByMarket(context.Background(), "x")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].Amount != 0 {
		t.Fatalf("null amount → 0 expected, got %v", out[0].Amount)
	}
}

func TestParse_EmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	out, _, err := c.GetHoldersByMarket(context.Background(), "x")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty, got %v", out)
	}
}

func TestRetryOn429(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&hits, 1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	_, _, err := c.GetHoldersByMarket(context.Background(), "x")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if atomic.LoadInt64(&hits) < 2 {
		t.Fatalf("expected retry; hits=%d", hits)
	}
}

func TestNoRetryOn400(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	_, _, err := c.GetHoldersByMarket(context.Background(), "x")
	if err == nil {
		t.Fatalf("expected error on 400")
	}
	if atomic.LoadInt64(&hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", hits)
	}
}

func TestContextTimeoutPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := c.GetHoldersByMarket(ctx, "x")
	if err == nil {
		t.Fatalf("expected context timeout")
	}
}

func TestInvalidJSONReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{not json`))
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	_, _, err := c.GetHoldersByMarket(context.Background(), "x")
	if err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestPathsOverride(t *testing.T) {
	var lastPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath = r.URL.Path
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := NewWithPaths(polymarket.New(srv.URL, 10, time.Second), Paths{
		Holders:   "/v2/holders",
		Positions: "/v2/positions",
		Trades:    "/v2/trades",
		Activity:  "/v2/activity",
		Traded:    "/v2/traded",
		Value:     "/v2/value",
	})
	_, _, _ = c.GetHoldersByMarket(context.Background(), "x")
	if lastPath != "/v2/holders" {
		t.Fatalf("expected /v2/holders, got %s", lastPath)
	}
}

func TestPathsOverride_FullSet(t *testing.T) {
	// ensures all six endpoints honor the path override; serves empty payloads
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/traded":
			w.Write([]byte(`{"user":"x","traded":7}`))
		case "/v2/value":
			w.Write([]byte(`[{"user":"x","value":"10"}]`))
		default:
			w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()
	c := NewWithPaths(polymarket.New(srv.URL, 10, time.Second), Paths{
		Holders: "/v2/holders", Positions: "/v2/positions",
		Trades: "/v2/trades", Activity: "/v2/activity",
		Traded: "/v2/traded", Value: "/v2/value",
	})
	if _, _, err := c.GetUserPositions(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.GetTrades(context.Background(), "x", "", false, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.GetActivity(context.Background(), "x", 0); err != nil {
		t.Fatal(err)
	}
	n, _, err := c.GetTradedCount(context.Background(), "x")
	if err != nil || n != 7 {
		t.Fatalf("traded got %d err=%v", n, err)
	}
	v, _, err := c.GetUserValue(context.Background(), "x")
	if err != nil || v != 10 {
		t.Fatalf("value got %v err=%v", v, err)
	}
}

// sanity that real fixture decodes without error
func TestFixturesDecodeWithoutError(t *testing.T) {
	cases := map[string]any{
		"../testdata/dataapi_holders.json":   &[]HoldersGroup{},
		"../testdata/dataapi_positions.json": &[]Position{},
		"../testdata/dataapi_trades.json":    &[]Trade{},
		"../testdata/dataapi_activity.json":  &[]Activity{},
		"../testdata/dataapi_traded.json":    &tradedResp{},
	}
	for path, target := range cases {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
}
