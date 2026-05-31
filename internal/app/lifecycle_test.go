//go:build integration

// Integration tests for the open → exit lifecycle and the AlertingEnabled
// kill-switch. Requires INTEGRATION_DB_URL.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/marketscan"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/clob"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
	"github.com/Borislavv/polymarket-sharks/internal/telegram"
	"github.com/Borislavv/polymarket-sharks/internal/walletintel"
)

// fakeActivity returns a fixed activity row (used to inject open then exit).
type lifecycleFakeAPI struct {
	mu       sync.Mutex   // unused but kept for future race-free state
	activity atomic.Value // []byte
}

func newLifecycleAPI() *lifecycleFakeAPI {
	f := &lifecycleFakeAPI{}
	f.activity.Store([]byte(`[]`))
	return f
}

func (f *lifecycleFakeAPI) set(body []byte) { f.activity.Store(body) }

func (f *lifecycleFakeAPI) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/activity":
			// Return injected activity only when the requested user matches
			// the proxyWallet inside the payload. Prevents bleed-through where
			// the watched-wallet worker would see another wallet's activity
			// (which doesn't happen in real /activity).
			body := f.activity.Load().([]byte)
			user := r.URL.Query().Get("user")
			if user != "" && !bodyMatchesUser(body, user) {
				w.Write([]byte(`[]`))
				return
			}
			w.Write(body)
		case "/trades":
			w.Write([]byte(`[]`))
		case "/positions":
			w.Write([]byte(`[]`))
		case "/traded":
			w.Write([]byte(`{"user":"x","traded":50}`))
		default:
			w.Write([]byte(`[]`))
		}
	}
}

func bodyMatchesUser(body []byte, user string) bool {
	var arr []map[string]any
	if err := json.Unmarshal(body, &arr); err != nil || len(arr) == 0 {
		return false
	}
	p, _ := arr[0]["proxyWallet"].(string)
	return p == user
}

func seedLifecycleEnv(t *testing.T, alertingEnabled bool) (*postgres.Store, *fakeTelegram, *alerts.Router, *walletintel.WatchedWalletWorker, *lifecycleFakeAPI, func()) {
	t.Helper()
	store := newTestStore(t)
	// event + market + token
	eid, _ := store.UpsertEvent(context.Background(), postgres.Event{Slug: "e-life", Title: "Life", Active: true})
	mid, _ := store.UpsertMarket(context.Background(), postgres.Market{ConditionID: "0xLC1", EventID: eid, Slug: "m-life", Question: "Live?", Active: true})
	_ = store.UpsertMarketToken(context.Background(), mid, postgres.MarketToken{OutcomeIndex: 0, OutcomeName: "Yes", ClobTokenID: "tok-yes"})
	// watched shark wallet
	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xshark", Pseudonym: "Shark"})
	_ = store.UpsertWatchlist(context.Background(), postgres.WatchlistRow{
		WalletID: wid, Class: "shark", Status: "active", Score: 80, Confidence: 0.8,
		FeatureSnapshot: map[string]any{}, ScoreVersion: walletintel.ScoreVersion,
	})

	api := newLifecycleAPI()
	ds := httptest.NewServer(api.handler())
	dataCli := dataapi.New(polymarket.New(ds.URL, 200, 2*time.Second))

	tg := &fakeTelegram{}
	tgSrv := httptest.NewServer(tg.handler())
	tgCli := telegram.New("t", 200, time.Second)
	tgCli.HTTP = &http.Client{Transport: rewriteHostTransport{base: tgSrv.URL}, Timeout: time.Second}

	router := alerts.NewRouter(store, tgCli, alerts.DefaultLinks(), slog.Default(), "admin", "bets", "clusters", "news")
	router.AlertingEnabled = alertingEnabled

	runner := &walletintel.Runner{
		DataAPI: dataCli, Store: store, Log: slog.Default(),
		SharkParams:   walletintel.SharkParams{MinTrades: 1, MinScore: 1, MinConfidence: 0.01, MaxStaleDays: 365},
		InsiderParams: walletintel.InsiderParams{MaxLifetimeTrades: 3, MinNotionalUSD: 1, MinScore: 1, MinConfidence: 0.01, LowProbPriceThr: 0.2},
	}
	w := &walletintel.WatchedWalletWorker{
		DataAPI: dataCli, Store: store, Router: router, Log: slog.Default(),
		Interval: time.Minute, Runner: runner, Links: alerts.DefaultLinks(),
		InsiderParams:            runner.InsiderParams,
		LifecycleEnabled:         true,
		ExitAlertsEnabled:        true,
		ExitFullCloseTolerance:   0.05,
		SharkAlertMinNotionalUSD: 1, // exercise alert path; dust filter tested separately
	}
	cleanup := func() { ds.Close(); tgSrv.Close() }
	return store, tg, router, w, api, cleanup
}

func TestLifecycle_OpenThenExitEmitsExitAlert(t *testing.T) {
	store, tg, _, w, api, cleanup := seedLifecycleEnv(t, true)
	defer cleanup()

	// 1) open BUY
	api.set([]byte(`[{"transactionHash":"0xOPEN1","proxyWallet":"0xshark","asset":"tok-yes","conditionId":"0xLC1",
	  "side":"BUY","outcome":"Yes","price":0.30,"size":50000,"usdcSize":15000,"timestamp":` + epoch(time.Now().Add(-2*time.Hour)) + `,"type":"TRADE","eventSlug":"e-life","title":"Live?"}]`))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	go w.Run(ctx) //nolint:errcheck
	waitForCount(t, store, "SELECT count(*) FROM watched_position_lifecycle WHERE status='open'", 1)
	// NEW BET alert sent
	waitForCount(t, store, "SELECT count(*) FROM alert_decisions WHERE alert_type='SHARK_BET'", 1)
	cancel()

	// 2) exit SELL
	api.set([]byte(`[{"transactionHash":"0xEXIT1","proxyWallet":"0xshark","asset":"tok-yes","conditionId":"0xLC1",
	  "side":"SELL","outcome":"Yes","price":0.45,"size":50000,"usdcSize":22500,"timestamp":` + epoch(time.Now()) + `,"type":"TRADE","eventSlug":"e-life","title":"Live?"}]`))
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	go w.Run(ctx2) //nolint:errcheck
	waitForCount(t, store, "SELECT count(*) FROM watched_position_lifecycle WHERE status='closed'", 1)
	waitForCount(t, store, "SELECT count(*) FROM alert_decisions WHERE alert_type='POSITION_EXIT'", 1)
	cancel2()
	if len(tg.received()) < 2 {
		t.Fatalf("expected open+exit alerts via Telegram, got %d", len(tg.received()))
	}
}

func TestLifecycle_DuplicateExitNotResent(t *testing.T) {
	store, tg, _, w, api, cleanup := seedLifecycleEnv(t, true)
	defer cleanup()
	api.set([]byte(`[{"transactionHash":"0xOPEN2","proxyWallet":"0xshark","asset":"tok-yes","conditionId":"0xLC1",
	  "side":"BUY","outcome":"Yes","price":0.30,"size":50000,"usdcSize":15000,"timestamp":` + epoch(time.Now().Add(-2*time.Hour)) + `,"type":"TRADE","eventSlug":"e-life","title":"Live?"}]`))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	go w.Run(ctx) //nolint:errcheck
	waitForCount(t, store, "SELECT count(*) FROM watched_position_lifecycle", 1)
	cancel()

	// fire same exit twice (different polling cycles)
	api.set([]byte(`[{"transactionHash":"0xEXIT2","proxyWallet":"0xshark","asset":"tok-yes","conditionId":"0xLC1",
	  "side":"SELL","outcome":"Yes","price":0.45,"size":50000,"usdcSize":22500,"timestamp":` + epoch(time.Now()) + `,"type":"TRADE","eventSlug":"e-life","title":"Live?"}]`))
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		go w.Run(ctx) //nolint:errcheck
		<-ctx.Done()
		cancel()
	}
	var n int
	if err := store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM alert_decisions WHERE alert_type='POSITION_EXIT'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 exit alert, got %d (tg=%d)", n, len(tg.received()))
	}
}

func TestLifecycle_NonWatchedWalletIgnored(t *testing.T) {
	// API returns activity for 0xUNKNOWN. The worker iterates only watched
	// wallets ({0xshark}); /activity?user=0xshark returns []. No lifecycle.
	store, _, _, w, api, cleanup := seedLifecycleEnv(t, true)
	defer cleanup()
	api.set([]byte(`[{"transactionHash":"0xOPENX","proxyWallet":"0xUNKNOWN","asset":"tok-yes","conditionId":"0xLC1",
	  "side":"BUY","outcome":"Yes","price":0.30,"size":50000,"usdcSize":15000,"timestamp":` + epoch(time.Now()) + `,"type":"TRADE","eventSlug":"e-life","title":"Live?"}]`))
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	go w.Run(ctx) //nolint:errcheck
	<-ctx.Done()
	cancel()
	var n int
	store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM watched_position_lifecycle`).Scan(&n)
	if n != 0 {
		t.Fatalf("non-watched wallet must not create lifecycle, got %d", n)
	}
	// And no watched_bets either.
	store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM watched_bets`).Scan(&n)
	if n != 0 {
		t.Fatalf("non-watched wallet must not create watched_bets, got %d", n)
	}
}

func TestLifecycle_AlertingDisabledPersistsSkipped(t *testing.T) {
	store, tg, _, w, api, cleanup := seedLifecycleEnv(t, false)
	defer cleanup()
	api.set([]byte(`[{"transactionHash":"0xOPEN3","proxyWallet":"0xshark","asset":"tok-yes","conditionId":"0xLC1",
	  "side":"BUY","outcome":"Yes","price":0.30,"size":50000,"usdcSize":15000,"timestamp":` + epoch(time.Now()) + `,"type":"TRADE","eventSlug":"e-life","title":"Live?"}]`))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	go w.Run(ctx) //nolint:errcheck
	waitForCount(t, store, "SELECT count(*) FROM alert_decisions", 1)
	cancel()
	if len(tg.received()) != 0 {
		t.Fatalf("alerting disabled: Telegram must NOT receive, got %d", len(tg.received()))
	}
	var status string
	store.Pool.QueryRow(context.Background(), `SELECT status FROM telegram_deliveries LIMIT 1`).Scan(&status)
	if status != "skipped" {
		t.Fatalf("expected delivery status 'skipped', got %q", status)
	}
}

func TestWSConsumer_UpdatesMarketStateAndSamples(t *testing.T) {
	store := newTestStore(t)
	eid, _ := store.UpsertEvent(context.Background(), postgres.Event{Slug: "ws-e", Title: "WS", Active: true})
	mid, _ := store.UpsertMarket(context.Background(), postgres.Market{ConditionID: "0xWS1", EventID: eid, Slug: "ws-m", Active: true})
	_ = store.UpsertMarketToken(context.Background(), mid, postgres.MarketToken{OutcomeIndex: 0, OutcomeName: "Yes", ClobTokenID: "tok-ws"})

	cache := marketscan.NewTokenCache(store)
	c := &marketscan.WSConsumer{Store: store, Cache: cache, Log: slog.Default()}

	// 1) book snapshot
	raw := json.RawMessage(`{"event_type":"book","asset_id":"tok-ws","bids":[{"price":"0.30"}],"asks":[{"price":"0.34"}]}`)
	c.HandleEvent(clob.WSEvent{EventType: "book", AssetID: "tok-ws", Raw: raw})
	// 2) price_change BUY
	raw2 := json.RawMessage(`{"event_type":"price_change","asset_id":"tok-ws","price":"0.36","side":"BUY"}`)
	c.HandleEvent(clob.WSEvent{EventType: "price_change", AssetID: "tok-ws", Raw: raw2})
	// 3) unknown asset id is silently ignored
	c.HandleEvent(clob.WSEvent{EventType: "book", AssetID: "tok-UNKNOWN", Raw: raw})

	// verify market_state
	var lastPrice, bestBid, bestAsk float64
	store.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(last_price,0), COALESCE(best_bid,0), COALESCE(best_ask,0) FROM market_state WHERE market_id=$1::uuid`,
		mid).Scan(&lastPrice, &bestBid, &bestAsk)
	if bestAsk == 0 || bestBid == 0 {
		t.Fatalf("market_state not updated: bid=%v ask=%v last=%v", bestBid, bestAsk, lastPrice)
	}
	// verify price samples
	var samples int
	store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM market_price_samples WHERE market_id=$1::uuid AND source='ws'`,
		mid).Scan(&samples)
	if samples < 1 {
		t.Fatalf("expected >=1 ws price samples, got %d", samples)
	}
	// verify wallet_trades / watched_bets are NOT created from WS
	var trades, watched int
	store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM wallet_trades`).Scan(&trades)
	store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM watched_bets`).Scan(&watched)
	if trades != 0 || watched != 0 {
		t.Fatalf("WS must not create wallet_trades/watched_bets: trades=%d watched=%d", trades, watched)
	}
}

// helpers

func epoch(t time.Time) string { return fmt.Sprintf("%d", t.Unix()) }

func waitForCount(t *testing.T, store *postgres.Store, q string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := store.Pool.QueryRow(context.Background(), q).Scan(&n); err == nil && n == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	var n int
	store.Pool.QueryRow(context.Background(), q).Scan(&n)
	t.Fatalf("waitForCount: %q expected %d got %d", q, want, n)
}

// avoid "imported and not used" if some symbol is conditionally referenced
var _ = sync.Mutex{}
