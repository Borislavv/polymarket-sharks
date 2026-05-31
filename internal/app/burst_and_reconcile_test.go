//go:build integration

// Integration tests for incident-driven fixes:
//   - dust suppression for individual SHARK_BET alerts
//   - single-trader burst aggregation
//   - exit trades excluded from many-trader cluster source
//   - stale shark watchlist reconciliation
package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
	"github.com/Borislavv/polymarket-sharks/internal/telegram"
	"github.com/Borislavv/polymarket-sharks/internal/walletintel"
)

func seedBurstEnv(t *testing.T) (*postgres.Store, *fakeTelegram, *alerts.Router, *lifecycleFakeAPI, func()) {
	t.Helper()
	store := newTestStore(t)
	eid, _ := store.UpsertEvent(context.Background(), postgres.Event{Slug: "e-burst", Title: "Burst", Active: true})
	mid, _ := store.UpsertMarket(context.Background(), postgres.Market{ConditionID: "0xBURST1", EventID: eid, Slug: "m-burst", Question: "Burst?", Active: true})
	_ = store.UpsertMarketToken(context.Background(), mid, postgres.MarketToken{OutcomeIndex: 0, OutcomeName: "Yes", ClobTokenID: "tok-yes"})
	mid2, _ := store.UpsertMarket(context.Background(), postgres.Market{ConditionID: "0xBURST2", EventID: eid, Slug: "m-burst2", Question: "B2?", Active: true})
	_ = store.UpsertMarketToken(context.Background(), mid2, postgres.MarketToken{OutcomeIndex: 0, OutcomeName: "Yes", ClobTokenID: "tok-yes-2"})

	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xburst-shark", Pseudonym: "B-Shark"})
	_ = store.UpsertWatchlist(context.Background(), postgres.WatchlistRow{
		WalletID: wid, Class: "shark", Status: "active", Score: 80, Confidence: 0.8,
		FeatureSnapshot: map[string]any{}, ScoreVersion: walletintel.ScoreVersion,
	})

	api := newLifecycleAPI()
	ds := httptest.NewServer(api.handler())
	tg := &fakeTelegram{}
	tgSrv := httptest.NewServer(tg.handler())
	tgCli := telegram.New("t", 200, time.Second)
	tgCli.HTTP = &http.Client{Transport: rewriteHostTransport{base: tgSrv.URL}, Timeout: time.Second}
	router := alerts.NewRouter(store, tgCli, alerts.DefaultLinks(), slog.Default(), "admin", "bets", "clusters", "news")
	router.AlertingEnabled = true

	_ = dataapi.New(polymarket.New(ds.URL, 200, 2*time.Second)) // ds is used by /activity polling if needed
	cleanup := func() { ds.Close(); tgSrv.Close() }
	return store, tg, router, api, cleanup
}

func TestDustSharkBet_NoIndividualAlert(t *testing.T) {
	store, tg, router, api, cleanup := seedBurstEnv(t)
	defer cleanup()
	dataCli := dataapi.New(polymarket.New(api.activityURL(t), 200, 2*time.Second))
	w := &walletintel.WatchedWalletWorker{
		DataAPI: dataCli, Store: store, Router: router, Log: slog.Default(),
		Interval: time.Minute, Links: alerts.DefaultLinks(),
		LifecycleEnabled:         true,
		ExitAlertsEnabled:        true,
		ExitFullCloseTolerance:   0.05,
		SharkAlertMinNotionalUSD: 10_000,
	}
	api.set([]byte(`[{"transactionHash":"0xdust1","proxyWallet":"0xburst-shark","asset":"tok-yes","conditionId":"0xBURST1",
	  "side":"BUY","outcome":"Yes","price":0.01,"size":50,"usdcSize":0.5,"timestamp":` + epoch(time.Now()) + `,"type":"TRADE","eventSlug":"e-burst","title":"Burst?"}]`))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	go w.Run(ctx) //nolint:errcheck
	waitForCount(t, store, "SELECT count(*) FROM watched_bets WHERE bet_kind='entry'", 1)
	cancel()
	if len(tg.received()) != 0 {
		t.Fatalf("expected NO individual user alert for dust bet, got %d", len(tg.received()))
	}
	var n int
	store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM alert_decisions WHERE alert_type='SHARK_BET'`).Scan(&n)
	if n != 0 {
		t.Fatalf("expected NO SHARK_BET decision for dust, got %d", n)
	}
}

func TestSingleTraderBurst_AggregatesIntoOneAlert(t *testing.T) {
	store, tg, router, _, cleanup := seedBurstEnv(t)
	defer cleanup()
	// 5 entry bets from the same shark on two markets, all within 15m
	var wid string
	store.Pool.QueryRow(context.Background(), `SELECT id::text FROM wallets WHERE proxy_wallet='0xburst-shark'`).Scan(&wid)
	var mid1, mid2 string
	store.Pool.QueryRow(context.Background(), `SELECT id::text FROM markets WHERE condition_id='0xBURST1'`).Scan(&mid1)
	store.Pool.QueryRow(context.Background(), `SELECT id::text FROM markets WHERE condition_id='0xBURST2'`).Scan(&mid2)
	for i, mid := range []string{mid1, mid1, mid1, mid2, mid2} {
		tradeID, _, _ := store.InsertWalletTrade(context.Background(), postgres.WalletTrade{
			TransactionHash: "0xbst" + epoch(time.Now()) + ":" + epoch(time.Now().Add(time.Duration(i)*time.Second)),
			WalletID:        wid, MarketID: mid, ConditionID: "0xBURST" + epoch(time.Now()),
			Outcome: "Yes", Side: "BUY", Direction: "YES_BUY", Price: 0.01, Size: 50, UsdcSize: 0.5,
			Timestamp: time.Now(), Source: "test",
		})
		store.InsertWatchedBet(context.Background(), postgres.WatchedBet{
			WalletTradeID: tradeID, WalletID: wid, MarketID: mid, Direction: "YES_BUY",
			Notional: 0.5, Price: 0.01, Odds: 100, WalletClass: "shark", WalletScore: 80,
			FeatureSnapshot: map[string]any{}, DetectedAt: time.Now(), BetKind: "entry",
		})
	}
	bw := &walletintel.BurstWorker{
		Store: store, Router: router, Log: slog.Default(), Links: alerts.DefaultLinks(),
		Interval: time.Hour, Window: 15 * time.Minute, MinBets: 3, MinMarkets: 2,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	go bw.Run(ctx) //nolint:errcheck
	waitForCount(t, store, "SELECT count(*) FROM alert_decisions WHERE alert_type='SHARK_BURST'", 1)
	cancel()
	if len(tg.received()) != 1 {
		t.Fatalf("expected exactly 1 burst alert, got %d", len(tg.received()))
	}
	// run again — dedup
	ctx2, cancel2 := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	go bw.Run(ctx2) //nolint:errcheck
	<-ctx2.Done()
	cancel2()
	var n int
	store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM alert_decisions WHERE alert_type='SHARK_BURST'`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected burst dedup, got %d decisions", n)
	}
}

func TestExitsExcludedFromEntryClusterSource(t *testing.T) {
	store := newTestStore(t)
	eid, _ := store.UpsertEvent(context.Background(), postgres.Event{Slug: "e-x", Title: "E", Active: true})
	mid, _ := store.UpsertMarket(context.Background(), postgres.Market{ConditionID: "0xX", EventID: eid, Slug: "m", Active: true})
	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xexit"})
	tradeID, _, _ := store.InsertWalletTrade(context.Background(), postgres.WalletTrade{
		TransactionHash: "0xtx-x", WalletID: wid, MarketID: mid, ConditionID: "0xX",
		Outcome: "Yes", Side: "SELL", Direction: "YES_SELL", Price: 0.01, Size: 50, UsdcSize: 0.5,
		Timestamp: time.Now(), Source: "test",
	})
	store.InsertWatchedBet(context.Background(), postgres.WatchedBet{
		WalletTradeID: tradeID, WalletID: wid, MarketID: mid, Direction: "YES_SELL",
		Notional: 0.5, Price: 0.01, WalletClass: "shark", WalletScore: 80,
		FeatureSnapshot: map[string]any{}, DetectedAt: time.Now(), BetKind: "exit",
	})
	// IncludeExits=false → 0 rows
	rows, err := store.ListRecentWatchedBets(context.Background(), time.Now().Add(-time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected exits excluded by default, got %d", len(rows))
	}
	// IncludeExits=true → 1 row
	rows, _ = store.ListRecentWatchedBets(context.Background(), time.Now().Add(-time.Hour), true)
	if len(rows) != 1 {
		t.Fatalf("expected exit visible when includeExits=true, got %d", len(rows))
	}
}

func TestReconcile_DemotesStaleShark(t *testing.T) {
	store := newTestStore(t)
	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xstale"})
	// seed an active shark with the wrong score_version
	_ = store.UpsertWatchlist(context.Background(), postgres.WatchlistRow{
		WalletID: wid, Class: "shark", Status: "active", Score: 70, Confidence: 0.8,
		FeatureSnapshot: map[string]any{}, ScoreVersion: "v0.0.0-stale",
	})
	n, err := walletintel.ReconcileStaleSharks(context.Background(), store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 demotion, got %d", n)
	}
	var status, class string
	var reasons []string
	store.Pool.QueryRow(context.Background(),
		`SELECT status, class, reason_codes FROM wallet_watchlist WHERE wallet_id=$1::uuid`, wid).Scan(&status, &class, &reasons)
	if status != "rejected" || class != "rejected_stale" {
		t.Fatalf("expected rejected/rejected_stale, got %s/%s", status, class)
	}
	foundReason := false
	for _, r := range reasons {
		if r == "STALE_SCORE_VERSION" {
			foundReason = true
		}
	}
	if !foundReason {
		t.Fatalf("missing STALE_SCORE_VERSION reason")
	}
}

func TestStaleSharkSuppressedFromIndividualAlert(t *testing.T) {
	// Wallet on watchlist with stale score_version: even a >=10k bet must
	// NOT emit individual SHARK_BET (score-version guard).
	store, tg, router, api, cleanup := seedBurstEnv(t)
	defer cleanup()
	// override watchlist row to stale
	_ = store.UpsertWatchlist(context.Background(), postgres.WatchlistRow{
		WalletID: walletIDFor(t, store, "0xburst-shark"),
		Class:    "shark", Status: "active", Score: 80, Confidence: 0.8,
		FeatureSnapshot: map[string]any{}, ScoreVersion: "v0.0.0",
	})
	dataCli := dataapi.New(polymarket.New(api.activityURL(t), 200, 2*time.Second))
	w := &walletintel.WatchedWalletWorker{
		DataAPI: dataCli, Store: store, Router: router, Log: slog.Default(),
		Interval: time.Minute, Links: alerts.DefaultLinks(),
		LifecycleEnabled: true, ExitAlertsEnabled: true, ExitFullCloseTolerance: 0.05,
		SharkAlertMinNotionalUSD: 1, // would have allowed alert
	}
	api.set([]byte(`[{"transactionHash":"0xstaleAlert","proxyWallet":"0xburst-shark","asset":"tok-yes","conditionId":"0xBURST1",
	  "side":"BUY","outcome":"Yes","price":0.30,"size":50000,"usdcSize":15000,"timestamp":` + epoch(time.Now()) + `,"type":"TRADE","eventSlug":"e-burst","title":"Burst?"}]`))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	go w.Run(ctx) //nolint:errcheck
	waitForCount(t, store, "SELECT count(*) FROM watched_bets WHERE bet_kind='entry'", 1)
	cancel()
	if len(tg.received()) != 0 {
		t.Fatalf("stale-version shark must not emit user alert, got %d", len(tg.received()))
	}
}

func TestSharkPayloadUsesRealEvidence(t *testing.T) {
	store, tg, router, api, cleanup := seedBurstEnv(t)
	defer cleanup()
	wid := walletIDFor(t, store, "0xburst-shark")
	// store an old wallet_score row so GetLatestSharkEvidence finds it
	snapJSON, _ := json.Marshal(map[string]any{
		"total_trades":              420,
		"realized_pnl":              54321.0,
		"realized_pnl_known":        true,
		"win_rate":                  0.81,
		"closed_positions_count":    42,
		"roi":                       0.45,
		"avg_closed_position_stake": 18500.0,
		"promotion_path":            "historical_shark",
	})
	_, _ = store.Pool.Exec(context.Background(), `
		INSERT INTO wallet_scores (wallet_id, strategy, class, score, confidence, promote, score_version, feature_snapshot, reason_codes)
		VALUES ($1::uuid, 'shark_score', 'shark', 88, 0.77, true, $2, $3::jsonb, ARRAY['HIGH_SAMPLE_SIZE','POSITIVE_REALIZED_PNL'])
	`, wid, walletintel.ScoreVersion, string(snapJSON))
	// update watchlist to match current ScoreVersion (otherwise stale guard fires)
	_ = store.UpsertWatchlist(context.Background(), postgres.WatchlistRow{
		WalletID: wid, Class: "shark", Status: "active", Score: 88, Confidence: 0.77,
		FeatureSnapshot: map[string]any{}, ScoreVersion: walletintel.ScoreVersion,
	})

	dataCli := dataapi.New(polymarket.New(api.activityURL(t), 200, 2*time.Second))
	w := &walletintel.WatchedWalletWorker{
		DataAPI: dataCli, Store: store, Router: router, Log: slog.Default(),
		Interval: time.Minute, Links: alerts.DefaultLinks(),
		LifecycleEnabled: true, ExitAlertsEnabled: true, ExitFullCloseTolerance: 0.05,
		SharkAlertMinNotionalUSD: 1000,
	}
	api.set([]byte(`[{"transactionHash":"0xreal1","proxyWallet":"0xburst-shark","asset":"tok-yes","conditionId":"0xBURST1",
	  "side":"BUY","outcome":"Yes","price":0.30,"size":40000,"usdcSize":12000,"timestamp":` + epoch(time.Now()) + `,"type":"TRADE","eventSlug":"e-burst","title":"Burst?"}]`))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	go w.Run(ctx) //nolint:errcheck
	waitForCount(t, store, "SELECT count(*) FROM alert_decisions WHERE alert_type='SHARK_BET'", 1)
	cancel()
	if len(tg.received()) != 1 {
		t.Fatalf("expected 1 telegram alert, got %d", len(tg.received()))
	}
	body := tg.received()[0]
	// v4: payload renders historical closed-position evidence rather than
	// raw trade count. Assert v4 fields are present and the score path is shown.
	for _, need := range []string{"42 positions", "$54.3k", "81%", "ROI 45%", "historical shark"} {
		if !containsAfterEsc(body, need) {
			t.Fatalf("alert body missing %q\n---\n%s", need, body)
		}
	}
	if containsAfterEsc(body, "Closed: 0 positions") || containsAfterEsc(body, "confidence 0.00") {
		t.Fatalf("alert body contains stale-fake fields\n---\n%s", body)
	}
}

func walletIDFor(t *testing.T, store *postgres.Store, proxy string) string {
	t.Helper()
	var id string
	if err := store.Pool.QueryRow(context.Background(),
		`SELECT id::text FROM wallets WHERE proxy_wallet=$1`, proxy).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func containsAfterEsc(s, sub string) bool {
	// strip MarkdownV2 backslash escapes for substring assertions
	stripped := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			continue
		}
		stripped = append(stripped, s[i])
	}
	return indexOf(string(stripped), sub) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestReconcile_ArbitrationSnapshotDoesNotCauseFactDemotion proves the P0 fix:
// when watchlist holds an arbitration snapshot (no scoring keys), but
// wallet_scores.promote=true, ReconcileFailedSharks must NOT demote the wallet.
func TestReconcile_ArbitrationSnapshotDoesNotCauseFactDemotion(t *testing.T) {
	store := newTestStore(t)
	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xnodemote"})
	// Watchlist holds the arbitration snapshot — no closed_positions_complete / roi / win_rate.
	_, err := store.Pool.Exec(context.Background(), `
		INSERT INTO wallet_watchlist (wallet_id, class, status, score, confidence,
		    reason_codes, feature_snapshot, score_version, promoted_at, updated_at)
		VALUES ($1::uuid, 'shark', 'active', 80, 0.9, '{}',
		        '{"shark_score":80,"final_class":"shark"}'::jsonb, $2, now(), now())
	`, wid, walletintel.ScoreVersion)
	if err != nil {
		t.Fatal(err)
	}
	// wallet_scores: promote=true — source of truth for reconcile.
	validSnap, _ := json.Marshal(map[string]any{
		"closed_positions_count": 30, "win_rate": 0.80, "roi": 0.35,
		"avg_closed_position_stake": 15_000.0, "closed_positions_complete": true,
	})
	_, err = store.Pool.Exec(context.Background(), `
		INSERT INTO wallet_scores (wallet_id, strategy, class, score, confidence, promote,
		    score_version, feature_snapshot, reason_codes)
		VALUES ($1::uuid, 'shark_score', 'shark', 80, 0.9, true, $2, $3::jsonb,
		        ARRAY['SHARK_HISTORICAL_EDGE'])
	`, wid, walletintel.ScoreVersion, string(validSnap))
	if err != nil {
		t.Fatal(err)
	}
	n, err := walletintel.ReconcileFailedSharks(context.Background(), store, walletintel.SharkParams{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("wallet with promote=true in wallet_scores must NOT be demoted, got %d demotions", n)
	}
	var status string
	store.Pool.QueryRow(context.Background(),
		`SELECT status FROM wallet_watchlist WHERE wallet_id=$1::uuid`, wid).Scan(&status)
	if status != "active" {
		t.Fatalf("expected status=active, got %q", status)
	}
}

// TestReconcile_MissingScoreMarksNeedsRescore proves that a wallet with no
// wallet_scores row is marked needs_rescore — never demoted as PARTIAL_CLOSED_POSITION_HISTORY.
func TestReconcile_MissingScoreMarksNeedsRescore(t *testing.T) {
	store := newTestStore(t)
	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xnoscore"})
	_, err := store.Pool.Exec(context.Background(), `
		INSERT INTO wallet_watchlist (wallet_id, class, status, score, confidence,
		    reason_codes, feature_snapshot, score_version, promoted_at, updated_at)
		VALUES ($1::uuid, 'shark', 'active', 75, 0.85, '{}', '{}'::jsonb, $2, now(), now())
	`, wid, walletintel.ScoreVersion)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately do NOT insert any wallet_scores row.
	n, err := walletintel.ReconcileFailedSharks(context.Background(), store, walletintel.SharkParams{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("missing score row must not count as demotion, got %d", n)
	}
	var status, class string
	store.Pool.QueryRow(context.Background(),
		`SELECT status, class FROM wallet_watchlist WHERE wallet_id=$1::uuid`, wid).Scan(&status, &class)
	if status != "needs_rescore" {
		t.Fatalf("expected needs_rescore, got %q/%q", status, class)
	}
	if class == "rejected_unactual" {
		t.Fatalf("wallet must not be rejected_unactual for missing score row")
	}
}

// TestRestoreIncorrectlyDemotedSharks_RestoresValidUnactual proves that
// RestoreIncorrectlyDemotedSharks restores a wallet that is unactual in the
// watchlist but has promote=true in wallet_scores (the historical P0 scenario).
func TestRestoreIncorrectlyDemotedSharks_RestoresValidUnactual(t *testing.T) {
	store := newTestStore(t)
	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xrestore"})
	_, err := store.Pool.Exec(context.Background(), `
		INSERT INTO wallet_watchlist (wallet_id, class, status, score, confidence,
		    reason_codes, feature_snapshot, score_version, promoted_at, updated_at)
		VALUES ($1::uuid, 'rejected_unactual', 'unactual', 0, 0,
		        ARRAY['DEMOTED_UNACTUAL','SCORE_GATE_FAILED'], '{}'::jsonb, $2, now(), now())
	`, wid, walletintel.ScoreVersion)
	if err != nil {
		t.Fatal(err)
	}
	// wallet_scores: promote=true — wallet was incorrectly demoted.
	validSnap, _ := json.Marshal(map[string]any{
		"closed_positions_count": 35, "win_rate": 0.82, "roi": 0.40,
		"avg_closed_position_stake": 12_000.0, "closed_positions_complete": true,
	})
	_, err = store.Pool.Exec(context.Background(), `
		INSERT INTO wallet_scores (wallet_id, strategy, class, score, confidence, promote,
		    score_version, feature_snapshot, reason_codes)
		VALUES ($1::uuid, 'shark_score', 'shark', 78, 0.88, true, $2, $3::jsonb,
		        ARRAY['SHARK_HISTORICAL_EDGE'])
	`, wid, walletintel.ScoreVersion, string(validSnap))
	if err != nil {
		t.Fatal(err)
	}
	n, err := walletintel.RestoreIncorrectlyDemotedSharks(context.Background(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 restore, got %d", n)
	}
	var status, class string
	store.Pool.QueryRow(context.Background(),
		`SELECT status, class FROM wallet_watchlist WHERE wallet_id=$1::uuid`, wid).Scan(&status, &class)
	if status != "active" || class != "shark" {
		t.Fatalf("expected active/shark after restore, got %s/%s", status, class)
	}
}

// activityURL is added on lifecycleFakeAPI to read URL for clients
func (f *lifecycleFakeAPI) activityURL(t *testing.T) string {
	t.Helper()
	// the test server was created in seedBurstEnv but its URL is not stored;
	// we set a fresh one and let the test pick it up via httptest.NewServer
	// in seedBurstEnv (the server is the one returned by handler()). We
	// expose its URL via a sentinel pattern: re-create the server here so
	// callers in this file can use the same handler over a new URL.
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return srv.URL
}
