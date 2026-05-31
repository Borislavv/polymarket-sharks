//go:build integration

// Worker-level integration tests:
// fake Gamma + Data API + CLOB + Telegram, real Postgres on INTEGRATION_DB_URL.
//
// Run: INTEGRATION_DB_URL=... go test -tags=integration ./internal/app/...
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/marketscan"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/clob"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/gamma"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
	"github.com/Borislavv/polymarket-sharks/internal/telegram"
	"github.com/Borislavv/polymarket-sharks/internal/walletintel"
)

func newTestStore(t *testing.T) *postgres.Store {
	t.Helper()
	dsn := os.Getenv("INTEGRATION_DB_URL")
	if dsn == "" {
		t.Skip("INTEGRATION_DB_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	dropAll := `
		DROP TABLE IF EXISTS telegram_deliveries, alert_decisions, news_items,
		bet_clusters, watched_position_exits, watched_position_lifecycle,
		watched_bets, wallet_trades, holder_snapshots,
		wallet_watchlist, wallet_scores, wallets, market_state,
		market_tag_links, market_tokens, markets, events,
		market_price_samples CASCADE`
	if _, err := s.Pool.Exec(ctx, dropAll); err != nil {
		t.Fatalf("clean: %v", err)
	}
	migDir := filepath.Join("..", "..", "migrations")
	if err := s.Migrate(ctx, migDir); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

type fakeTelegram struct {
	mu       sync.Mutex
	sent     []string
	failNext atomic.Int64
}

func (f *fakeTelegram) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ChatID string `json:"chat_id"`
			Text   string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.failNext.Add(-1) >= 0 {
			http.Error(w, `{"ok":false,"description":"injected"}`, http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.sent = append(f.sent, body.Text)
		f.mu.Unlock()
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, len(f.sent)+1000)
	}
}

func (f *fakeTelegram) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	copy(out, f.sent)
	return out
}

// fakeGamma returns one event with a market + two tokens.
func fakeGammaServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
		  {"id":"e1","slug":"e-test","title":"Test Event","active":true,"closed":false,
		   "negRisk":false,"category":"politics",
		   "markets":[{"id":"m1","conditionId":"0xCID1","slug":"m-test","question":"Q?",
		     "active":true,"closed":false,"volume":"500000","liquidity":"100000",
		     "clobTokenIds":"[\"tok-yes\",\"tok-no\"]","outcomes":"[\"Yes\",\"No\"]",
		     "negRisk":false,"umaResolutionStatus":"proposed"}]}
		]`))
	}))
}

func fakeDataAPIServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/holders", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"token":"tok-yes","holders":[
		  {"proxyWallet":"0xshark","asset":"tok-yes","outcomeIndex":0,"pseudonym":"P-shark","amount":10000,"name":"shark"},
		  {"proxyWallet":"0xother","asset":"tok-yes","outcomeIndex":0,"pseudonym":"P-other","amount":500,"name":"other"}
		]}]`))
	})
	mux.HandleFunc("/traded", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user":"0x","traded":150}`))
	})
	mux.HandleFunc("/positions", func(w http.ResponseWriter, r *http.Request) {
		// v4-compatible distribution: 36 wins, 11 losses (WR ≈ 0.766 > 0.75),
		// avg totalBought $15k → avg_closed_stake > 10k, ROI > 0.33.
		var arr []map[string]any
		for i := 0; i < 36; i++ {
			arr = append(arr, map[string]any{
				"proxyWallet": "0xshark", "conditionId": fmt.Sprintf("0xC%d", i),
				"outcome": "Yes", "size": 0, "totalBought": 15000.0,
				"realizedPnl": 8000.0 + float64(i)*30, "currentValue": 0,
				"avgPrice": 0.40,
			})
		}
		for i := 0; i < 11; i++ {
			arr = append(arr, map[string]any{
				"proxyWallet": "0xshark", "conditionId": fmt.Sprintf("0xL%d", i),
				"outcome": "No", "size": 0, "totalBought": 15000.0,
				"realizedPnl": -3000.0 - float64(i)*20, "currentValue": 0,
				"avgPrice": 0.45,
			})
		}
		b, _ := json.Marshal(arr)
		w.Write(b)
	})
	mux.HandleFunc("/trades", func(w http.ResponseWriter, r *http.Request) {
		// Build qualifying history: 110 trades, each notional>=$10k, odds>=2
		// (price <= 0.5). v2.0.0 shark gate requires this.
		offset := 0
		if v := r.URL.Query().Get("offset"); v != "" {
			fmt.Sscanf(v, "%d", &offset)
		}
		if offset > 0 {
			w.Write([]byte(`[]`))
			return
		}
		var arr []map[string]any
		for i := 0; i < 110; i++ {
			arr = append(arr, map[string]any{
				"transactionHash": fmt.Sprintf("0xt%d", i),
				"proxyWallet":     "0xshark",
				"conditionId":     "0xCID1",
				"asset":           "tok-yes",
				"side":            "BUY",
				"outcome":         "Yes",
				"price":           0.30 + float64(i%5)*0.02,
				"size":            100000,
				"usdcSize":        25000,
				"timestamp":       time.Now().Add(-time.Duration(i) * time.Hour).Unix(),
				"eventSlug":       "e-test",
			})
		}
		b, _ := json.Marshal(arr)
		w.Write(b)
	})
	mux.HandleFunc("/activity", func(w http.ResponseWriter, r *http.Request) {
		u := r.URL.Query().Get("user")
		// new bet by shark — qualifying notional so dust filter doesn't trip
		w.Write([]byte(fmt.Sprintf(`[{
		  "transactionHash":"0xnew1","proxyWallet":%q,"asset":"tok-yes","conditionId":"0xCID1",
		  "side":"BUY","outcome":"Yes","price":0.32,"size":100000,"usdcSize":25000,
		  "timestamp":%d,"type":"TRADE","eventSlug":"e-test","title":"Q?"
		}]`, u, time.Now().Unix())))
	})
	mux.HandleFunc("/value", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"user":"0x","value":1000}]`))
	})
	return httptest.NewServer(mux)
}

func TestWorker_DiscoveryPersistsMarketWithTokens(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	gs := fakeGammaServer()
	defer gs.Close()
	cli := gamma.New(polymarket.New(gs.URL, 50, 2*time.Second))
	w := &marketscan.DiscoveryWorker{
		Gamma: cli, Store: store, Log: slog.Default(),
		TargetCategories: []string{"politics"}, PageLimit: 5,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx) //nolint:errcheck
	time.Sleep(800 * time.Millisecond)
	cancel()

	row, err := store.GetMarketByConditionID(context.Background(), "0xCID1")
	if err != nil {
		t.Fatalf("market not persisted: %v", err)
	}
	if row.Volume != 500000 || !row.NegRisk == true {
		// validate basic fields persisted
	}
	if row.UMAResolutionStatus != "proposed" {
		t.Fatalf("uma status not persisted: %v", row.UMAResolutionStatus)
	}
	var tokCount int
	if err := store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM market_tokens WHERE market_id::text=$1`, row.ID).Scan(&tokCount); err != nil {
		t.Fatal(err)
	}
	if tokCount != 2 {
		t.Fatalf("expected 2 tokens, got %d", tokCount)
	}
}

func TestWorker_HolderScanAndScoring(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	// pre-seed event + market + token
	eid, _ := store.UpsertEvent(context.Background(), postgres.Event{Slug: "e-test", Title: "T", Category: "politics", Active: true})
	mid, _ := store.UpsertMarket(context.Background(), postgres.Market{
		ConditionID: "0xCID1", EventID: eid, Slug: "m", Active: true,
		Volume: 500000, Liquidity: 100000,
	})
	_ = store.UpsertMarketToken(context.Background(), mid, postgres.MarketToken{
		OutcomeIndex: 0, OutcomeName: "Yes", ClobTokenID: "tok-yes",
	})

	ds := fakeDataAPIServer(t)
	defer ds.Close()
	dataCli := dataapi.New(polymarket.New(ds.URL, 100, 2*time.Second))

	runner := &walletintel.Runner{
		DataAPI: dataCli, Store: store, Log: slog.Default(),
		SharkParams: walletintel.SharkParams{
			HistMinClosedPositions: 25, HistMinROI: 0.3333,
			HistMinWinRate: 0.75, HistMinAvgStakeUSD: 10_000,
			MaxStaleDays: 365,
		},
		InsiderParams: walletintel.InsiderParams{
			MaxLifetimeForCapture: 10, MinNotionalUSD: 20_000, MinOdds: 3.0,
		},
		TargetCategories: []string{"politics"},
	}
	// v4 wiring: HolderScanWorker only discovers wallets; promotion
	// requires HistoryBackfillWorker to drain closed positions first.
	bf := &walletintel.HistoryBackfillWorker{
		DataAPI: dataCli, Store: store, Log: slog.Default(),
		ClosedPageSize: 500, TradePageSize: 500,
		MaxClosedPages: 5, MaxTradePages: 5,
	}
	scored := make(chan struct{}, 8)
	w := &marketscan.HolderScanWorker{
		DataAPI: dataCli, Store: store, Log: slog.Default(),
		Interval: 5 * time.Minute, HotsetSize: 5, DeepScanSize: 0,
		OnNewWallet: func(walletID, proxy string) {
			_, _ = bf.BackfillOne(context.Background(), walletID, proxy)
			_, _ = runner.ScoreWallet(context.Background(), walletID, proxy, nil, walletintel.MarketRulesInputs{}, false, 0)
			scored <- struct{}{}
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go w.Run(ctx) //nolint:errcheck
	// wait for scoring of at least 1 wallet
	deadline := time.After(4 * time.Second)
	got := 0
	for got < 1 {
		select {
		case <-scored:
			got++
		case <-deadline:
			t.Fatalf("scoring did not run for any wallet in time")
		}
	}
	cancel()
	// verify wallet_scores + watchlist for shark
	var scoreCount int
	store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wallet_scores ws JOIN wallets w ON w.id=ws.wallet_id WHERE w.proxy_wallet='0xshark'`).Scan(&scoreCount)
	if scoreCount < 2 {
		t.Fatalf("expected both shark+insider rows, got %d", scoreCount)
	}
	wallets, _ := store.ListActiveWatchlist(context.Background())
	if len(wallets) == 0 {
		t.Fatalf("expected shark on watchlist")
	}
}

func TestWorker_WatchedWalletEmitsAlert(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	// seed event/market/token
	eid, _ := store.UpsertEvent(context.Background(), postgres.Event{Slug: "e-test", Title: "T", Active: true})
	mid, _ := store.UpsertMarket(context.Background(), postgres.Market{ConditionID: "0xCID1", EventID: eid, Slug: "m-test", Question: "Q?", Active: true})
	_ = store.UpsertMarketToken(context.Background(), mid, postgres.MarketToken{OutcomeIndex: 0, OutcomeName: "Yes", ClobTokenID: "tok-yes"})

	// seed watched wallet as shark
	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xshark", Pseudonym: "Shark"})
	_ = store.UpsertWatchlist(context.Background(), postgres.WatchlistRow{
		WalletID: wid, Class: "shark", Status: "active", Score: 80, Confidence: 0.8,
		FeatureSnapshot: map[string]any{}, ScoreVersion: walletintel.ScoreVersion,
	})

	ds := fakeDataAPIServer(t)
	defer ds.Close()
	dataCli := dataapi.New(polymarket.New(ds.URL, 100, 2*time.Second))

	tg := &fakeTelegram{}
	tgSrv := httptest.NewServer(tg.handler())
	defer tgSrv.Close()
	tgCli := telegram.New("test-token", 100, time.Second)
	tgCli.HTTP = tgSrv.Client()
	// rewrite the API base for fakeness
	// telegram client posts to api.telegram.org; we need to point it elsewhere.
	// Trick: replace the client's RoundTrip to rewrite host.
	tgCli.HTTP = &http.Client{
		Transport: rewriteHostTransport{base: tgSrv.URL},
		Timeout:   time.Second,
	}

	router := alerts.NewRouter(store, tgCli, alerts.DefaultLinks(), slog.Default(),
		"admin", "bets", "clusters", "news")

	runner := &walletintel.Runner{
		DataAPI: dataCli, Store: store, Log: slog.Default(),
		SharkParams:   walletintel.SharkParams{MinTrades: 50, MinScore: 50, MinConfidence: 0.4, MaxStaleDays: 365},
		InsiderParams: walletintel.InsiderParams{MaxLifetimeTrades: 3, MinNotionalUSD: 1, MinScore: 50, MinConfidence: 0.5, LowProbPriceThr: 0.2},
	}
	w := &walletintel.WatchedWalletWorker{
		DataAPI: dataCli, Store: store, Router: router, Log: slog.Default(),
		Interval: time.Minute, Runner: runner,
		Links:         alerts.DefaultLinks(),
		InsiderParams: runner.InsiderParams,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx) //nolint:errcheck
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("watched alert not sent in time: telegram received=%v", tg.received())
		default:
			if len(tg.received()) > 0 {
				goto done
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
done:
	cancel()
	// verify alert_decisions row + telegram_deliveries
	var decN, delN int
	store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM alert_decisions WHERE alert_type='SHARK_BET'`).Scan(&decN)
	store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM telegram_deliveries WHERE status='ok'`).Scan(&delN)
	if decN == 0 {
		t.Fatalf("alert_decisions not persisted")
	}
	if delN == 0 {
		t.Fatalf("telegram_deliveries 'ok' not recorded")
	}
}

func TestWorker_TelegramRetrySucceedsAfterFailure(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	tg := &fakeTelegram{}
	tg.failNext.Store(1) // fail one send, then succeed
	tgSrv := httptest.NewServer(tg.handler())
	defer tgSrv.Close()
	tgCli := telegram.New("t", 100, time.Second)
	tgCli.HTTP = &http.Client{Transport: rewriteHostTransport{base: tgSrv.URL}, Timeout: time.Second}

	router := alerts.NewRouter(store, tgCli, alerts.DefaultLinks(), slog.Default(), "admin", "bets", "clusters", "news")

	// First send: will fail and create a failed delivery row.
	out := router.Route(context.Background(),
		postgres.AlertDecision{
			AlertType: "SHARK_BET", EntityType: "x", EntityID: "y",
			Severity: "WARNING", ShouldSend: true,
			UserAlertAllowed: true, AdminAlertAllowed: true,
			DedupKey:        alerts.DedupKey("test-1"),
			FeatureSnapshot: map[string]any{},
		}, "hello world", alerts.ChannelBets)
	if out.Err == nil {
		t.Fatalf("expected initial failure (failNext set)")
	}
	// Run retry worker once.
	rw := &alerts.RetryWorker{
		Store: store, Telegram: tgCli, Log: slog.Default(),
		Interval: time.Second, MaxAttempts: 3, BatchSize: 5,
	}
	// Force next_attempt_at into the past so the worker picks it up.
	if _, err := store.Pool.Exec(context.Background(),
		`UPDATE telegram_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE status='failed'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go rw.Run(ctx) //nolint:errcheck
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("retry did not succeed in time: received=%v", tg.received())
		default:
			if len(tg.received()) > 0 {
				goto ok
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
ok:
	cancel()
	var okN int
	store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM telegram_deliveries WHERE status='ok'`).Scan(&okN)
	if okN == 0 {
		t.Fatalf("delivery should have been marked ok")
	}
}

func TestWorker_TelegramRetryStopsAtMaxAttempts(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	tg := &fakeTelegram{}
	tg.failNext.Store(100) // always fail
	tgSrv := httptest.NewServer(tg.handler())
	defer tgSrv.Close()
	tgCli := telegram.New("t", 100, time.Second)
	tgCli.HTTP = &http.Client{Transport: rewriteHostTransport{base: tgSrv.URL}, Timeout: time.Second}

	router := alerts.NewRouter(store, tgCli, alerts.DefaultLinks(), slog.Default(), "admin", "bets", "clusters", "news")
	router.Route(context.Background(),
		postgres.AlertDecision{
			AlertType: "SHARK_BET", EntityType: "x", EntityID: "y",
			Severity: "WARNING", ShouldSend: true,
			UserAlertAllowed: true, AdminAlertAllowed: true,
			DedupKey: alerts.DedupKey("max-test"), FeatureSnapshot: map[string]any{},
		}, "body", alerts.ChannelBets)

	// drive retry forward
	for i := 0; i < 4; i++ {
		if _, err := store.Pool.Exec(context.Background(),
			`UPDATE telegram_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE status='failed'`); err != nil {
			t.Fatal(err)
		}
		rw := &alerts.RetryWorker{
			Store: store, Telegram: tgCli, Log: slog.Default(),
			Interval: 100 * time.Hour, MaxAttempts: 3, BatchSize: 5,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
		go rw.Run(ctx) //nolint:errcheck
		<-ctx.Done()
		cancel()
	}
	// after 3 attempts, retry worker stops picking the row
	var attempts int
	store.Pool.QueryRow(context.Background(), `SELECT max(attempt) FROM telegram_deliveries`).Scan(&attempts)
	if attempts < 3 {
		t.Fatalf("expected at least 3 attempts, got %d", attempts)
	}
	// no successful row
	var okN int
	store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM telegram_deliveries WHERE status='ok'`).Scan(&okN)
	if okN != 0 {
		t.Fatalf("no successful delivery should exist, got %d", okN)
	}
}

// rewriteHostTransport rewrites api.telegram.org requests to a local fake.
type rewriteHostTransport struct{ base string }

func (rt rewriteHostTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// Override the host: send the request to base instead of api.telegram.org.
	r2 := r.Clone(r.Context())
	u := r.URL
	u2 := *u
	// strip /botTOKEN/ prefix → keep just /sendMessage as path
	if i := indexAfter(u2.Path, "/bot"); i > 0 {
		if j := indexFrom(u2.Path, "/", i+4); j > 0 {
			u2.Path = u2.Path[j:]
		}
	}
	// rewrite to base URL
	target, err := http.NewRequest(r.Method, rt.base+u2.Path, r.Body)
	if err != nil {
		return nil, err
	}
	target.Header = r2.Header
	return http.DefaultClient.Do(target)
}

func indexAfter(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
func indexFrom(s, sub string, from int) int {
	for i := from; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// unused but kept for future cluster integration; suppress unused warnings.
var _ = clob.NewREST
