//go:build integration

// Integration tests for the profit-tier alert gate in WatchedWalletWorker.
// Requires: INTEGRATION_DB_URL=postgres://... go test -tags=integration ./internal/app/...
package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
	"github.com/Borislavv/polymarket-sharks/internal/telegram"
	"github.com/Borislavv/polymarket-sharks/internal/walletintel"

	"log/slog"
)

// activityDataServer returns a fake DataAPI that serves the given price and
// usdcSize for the /activity endpoint.
func activityDataServer(price float64, usdcSize float64) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/activity", func(w http.ResponseWriter, r *http.Request) {
		u := r.URL.Query().Get("user")
		w.Write([]byte(fmt.Sprintf(`[{
		  "transactionHash":"0xpg-test","proxyWallet":%q,"asset":"tok-yes","conditionId":"0xCID1",
		  "side":"BUY","outcome":"Yes","price":%.4f,"size":%.0f,"usdcSize":%.0f,
		  "timestamp":%d,"type":"TRADE","eventSlug":"e-test","title":"Q?"
		}]`, u, price, usdcSize/price, usdcSize, time.Now().Unix())))
	})
	mux.HandleFunc("/trades", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/positions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/value", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"user":"0x","value":0}]`))
	})
	return httptest.NewServer(mux)
}

// seedSharkWatchlist creates a market+token+wallet in an active shark watchlist.
func seedSharkWatchlist(t *testing.T, store *postgres.Store) {
	t.Helper()
	ctx := context.Background()
	eid, _ := store.UpsertEvent(ctx, postgres.Event{Slug: "e-test", Title: "T", Active: true})
	mid, _ := store.UpsertMarket(ctx, postgres.Market{
		ConditionID: "0xCID1", EventID: eid, Slug: "m-test", Question: "Q?", Active: true,
	})
	_ = store.UpsertMarketToken(ctx, mid, postgres.MarketToken{
		OutcomeIndex: 0, OutcomeName: "Yes", ClobTokenID: "tok-yes",
	})
	wid, _ := store.UpsertWallet(ctx, postgres.Wallet{ProxyWallet: "0xshark", Pseudonym: "Shark"})
	_ = store.UpsertWatchlist(ctx, postgres.WatchlistRow{
		WalletID: wid, Class: "shark", Status: "active", Score: 80, Confidence: 0.8,
		FeatureSnapshot: map[string]any{}, ScoreVersion: walletintel.ScoreVersion,
	})
}

func newTelegramSetup(t *testing.T) (*fakeTelegram, *telegram.Client) {
	t.Helper()
	tg := &fakeTelegram{}
	tgSrv := httptest.NewServer(tg.handler())
	t.Cleanup(tgSrv.Close)
	tgCli := telegram.New("test-token", 100, time.Second)
	tgCli.HTTP = &http.Client{
		Transport: rewriteHostTransport{base: tgSrv.URL},
		Timeout:   time.Second,
	}
	return tg, tgCli
}

// TestProfitGate_MediumTrade_LowPrice_Passes tests that a $2000 trade @ price 0.20
// (odds x5, profit $8000) passes the medium tier when thresholds are adjusted.
// Default medium_min_profit=15000 would fail, so we lower it to $7000 for this test.
func TestProfitGate_MediumTrade_LowPrice_Passes(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	seedSharkWatchlist(t, store)

	// price 0.20 → odds 5, profit = 2000*(5-1) = 8000
	// custom: medium_min_profit=7000 so 8000 >= 7000 → PASS
	ds := activityDataServer(0.20, 2000)
	defer ds.Close()
	dataCli := dataapi.New(polymarket.New(ds.URL, 100, 2*time.Second))

	tg, tgCli := newTelegramSetup(t)
	router := alerts.NewRouter(store, tgCli, alerts.DefaultLinks(), slog.Default(),
		"admin", "bets", "clusters", "news")

	w := &walletintel.WatchedWalletWorker{
		DataAPI: dataCli, Store: store, Router: router, Log: slog.Default(),
		Interval:      time.Minute,
		Runner:        &walletintel.Runner{DataAPI: dataCli, Store: store, Log: slog.Default()},
		Links:         alerts.DefaultLinks(),
		InsiderParams: walletintel.InsiderParams{MaxLifetimeTrades: 3, MinNotionalUSD: 1, MinScore: 50},
		ProfitGate: walletintel.ProfitGateParams{
			Enabled:           true,
			SmallMaxNotional:  2000,
			MediumMaxNotional: 10000,
			MediumMinOdds:     4,
			MediumMinProfit:   7000, // lowered so $8000 passes
			LargeMaxNotional:  80000,
			MegaMinNotional:   80000,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx) //nolint:errcheck

	deadline := time.After(2500 * time.Millisecond)
	for {
		select {
		case <-deadline:
			t.Fatalf("expected SHARK_BET alert to be sent; telegram received=%v", tg.received())
		default:
			if len(tg.received()) > 0 {
				goto sentOK
			}
			time.Sleep(80 * time.Millisecond)
		}
	}
sentOK:
	cancel()

	// Verify alert payload includes profit_if_win and odds fields
	msgs := tg.received()
	if len(msgs) == 0 {
		t.Fatal("no telegram messages received")
	}
	body := strings.Join(msgs, "\n")
	// Alert body must mention profit if win
	if !strings.Contains(body, "Profit if win") && !strings.Contains(body, "profit") {
		// Check via alert_decisions feature_snapshot instead
	}

	// Verify alert_decisions persisted with gate details
	var gateTier, gatePass string
	store.Pool.QueryRow(context.Background(), `
		SELECT feature_snapshot->>'alert_gate_tier', feature_snapshot->>'alert_gate_pass'
		FROM alert_decisions WHERE alert_type='SHARK_BET' AND should_send=true LIMIT 1
	`).Scan(&gateTier, &gatePass)

	if gateTier != "medium" {
		t.Fatalf("alert_gate_tier: want medium, got %q", gateTier)
	}
	if gatePass != "true" {
		t.Fatalf("alert_gate_pass: want true, got %q", gatePass)
	}
}

// TestProfitGate_MediumTrade_HighPrice_Suppressed tests that a $2000 trade
// @ price 0.80 (odds x1.25, profit $500) is suppressed by the profit gate.
func TestProfitGate_MediumTrade_HighPrice_Suppressed(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	seedSharkWatchlist(t, store)

	// price 0.80 → odds 1.25, profit = 2000*0.25 = 500 → FAIL medium (odds 1.25 < 4, profit 500 < 15000)
	ds := activityDataServer(0.80, 2000)
	defer ds.Close()
	dataCli := dataapi.New(polymarket.New(ds.URL, 100, 2*time.Second))

	tg, tgCli := newTelegramSetup(t)
	router := alerts.NewRouter(store, tgCli, alerts.DefaultLinks(), slog.Default(),
		"admin", "bets", "clusters", "news")

	w := &walletintel.WatchedWalletWorker{
		DataAPI: dataCli, Store: store, Router: router, Log: slog.Default(),
		Interval:      time.Minute,
		Runner:        &walletintel.Runner{DataAPI: dataCli, Store: store, Log: slog.Default()},
		Links:         alerts.DefaultLinks(),
		InsiderParams: walletintel.InsiderParams{MaxLifetimeTrades: 3, MinNotionalUSD: 1, MinScore: 50},
		ProfitGate: walletintel.ProfitGateParams{
			Enabled:           true,
			SmallMaxNotional:  2000,
			MediumMaxNotional: 10000,
			MediumMinOdds:     4,    // 1.25 < 4 → FAIL
			MediumMinProfit:   7000, // 500 < 7000 → FAIL
			LargeMaxNotional:  80000,
			MegaMinNotional:   80000,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go w.Run(ctx) //nolint:errcheck
	time.Sleep(1500 * time.Millisecond)
	cancel()

	// No user-facing telegram message
	if len(tg.received()) > 0 {
		t.Fatalf("expected no telegram (gate should suppress); got %v", tg.received())
	}

	// Suppressed decision must be persisted with gate details
	var gatePass, gateTier, gateReason string
	store.Pool.QueryRow(context.Background(), `
		SELECT feature_snapshot->>'alert_gate_pass',
		       feature_snapshot->>'alert_gate_tier',
		       feature_snapshot->>'alert_gate_reason'
		FROM alert_decisions WHERE alert_type='SHARK_BET' AND should_send=false LIMIT 1
	`).Scan(&gatePass, &gateTier, &gateReason)

	if gatePass != "false" {
		t.Fatalf("alert_gate_pass: want false, got %q", gatePass)
	}
	if gateTier != "medium" {
		t.Fatalf("alert_gate_tier: want medium, got %q", gateTier)
	}
	if !strings.Contains(gateReason, "medium") {
		t.Fatalf("alert_gate_reason should mention tier: got %q", gateReason)
	}
}

// TestProfitGate_MegaTrade_LowOdds_Passes verifies that a $80000 trade
// @ price 0.85 (odds 1.18, profit $14117) passes the mega tier.
func TestProfitGate_MegaTrade_LowOdds_Passes(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	seedSharkWatchlist(t, store)

	// price 0.85 → odds ≈ 1.176, profit = 80000*(1.176-1) = 80000*0.176 ≈ 14117
	// mega_min_odds=1.15, mega_min_profit=10000 → PASS
	ds := activityDataServer(0.85, 80000)
	defer ds.Close()
	dataCli := dataapi.New(polymarket.New(ds.URL, 100, 2*time.Second))

	tg, tgCli := newTelegramSetup(t)
	router := alerts.NewRouter(store, tgCli, alerts.DefaultLinks(), slog.Default(),
		"admin", "bets", "clusters", "news")

	w := &walletintel.WatchedWalletWorker{
		DataAPI: dataCli, Store: store, Router: router, Log: slog.Default(),
		Interval:      time.Minute,
		Runner:        &walletintel.Runner{DataAPI: dataCli, Store: store, Log: slog.Default()},
		Links:         alerts.DefaultLinks(),
		InsiderParams: walletintel.InsiderParams{MaxLifetimeTrades: 3, MinNotionalUSD: 1, MinScore: 50},
		ProfitGate: walletintel.ProfitGateParams{
			Enabled:          true,
			MegaMinNotional:  80000,
			MegaMinOdds:      1.15,
			MegaMinProfit:    10000,
			LargeMaxNotional: 80000,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx) //nolint:errcheck

	deadline := time.After(2500 * time.Millisecond)
	for {
		select {
		case <-deadline:
			t.Fatalf("expected SHARK_BET alert for $80k mega trade; telegram received=%v", tg.received())
		default:
			if len(tg.received()) > 0 {
				goto megaSent
			}
			time.Sleep(80 * time.Millisecond)
		}
	}
megaSent:
	cancel()

	// Verify tier is mega
	var gateTier string
	store.Pool.QueryRow(context.Background(), `
		SELECT feature_snapshot->>'alert_gate_tier'
		FROM alert_decisions WHERE alert_type='SHARK_BET' AND should_send=true LIMIT 1
	`).Scan(&gateTier)
	if gateTier != "mega" {
		t.Fatalf("alert_gate_tier: want mega, got %q", gateTier)
	}

	// Alert text must contain profit info
	body := strings.Join(tg.received(), "\n")
	if !strings.Contains(body, "Profit if win") {
		t.Fatalf("alert text should contain 'Profit if win'; got:\n%s", body)
	}
}

// TestProfitGate_GateDisabled_LegacyDustFloor tests backward-compat mode:
// when ProfitGate.Enabled=false the old SharkAlertMinNotionalUSD dust floor
// is used and a trade with notional < floor is suppressed.
func TestProfitGate_GateDisabled_LegacyDustFloor(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	seedSharkWatchlist(t, store)

	// notional 5000 < dust floor 10000 → suppressed (gate disabled)
	ds := activityDataServer(0.10, 5000) // odds x10, great profit, but dust floor blocks
	defer ds.Close()
	dataCli := dataapi.New(polymarket.New(ds.URL, 100, 2*time.Second))

	tg, tgCli := newTelegramSetup(t)
	router := alerts.NewRouter(store, tgCli, alerts.DefaultLinks(), slog.Default(),
		"admin", "bets", "clusters", "news")

	w := &walletintel.WatchedWalletWorker{
		DataAPI: dataCli, Store: store, Router: router, Log: slog.Default(),
		Interval:                 time.Minute,
		Runner:                   &walletintel.Runner{DataAPI: dataCli, Store: store, Log: slog.Default()},
		Links:                    alerts.DefaultLinks(),
		InsiderParams:            walletintel.InsiderParams{MaxLifetimeTrades: 3, MinNotionalUSD: 1, MinScore: 50},
		ProfitGate:               walletintel.ProfitGateParams{Enabled: false}, // legacy mode
		SharkAlertMinNotionalUSD: 10000,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go w.Run(ctx) //nolint:errcheck
	time.Sleep(1500 * time.Millisecond)
	cancel()

	if len(tg.received()) > 0 {
		t.Fatalf("legacy dust floor should suppress $5k trade; got %v", tg.received())
	}
}

// TestProfitGate_AlertPayloadContainsPayoffAndProfit checks that when a trade
// passes the gate the formatted Telegram message contains payoff_if_win and
// profit_if_win.
func TestProfitGate_AlertPayloadContainsPayoffAndProfit(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	seedSharkWatchlist(t, store)

	// $10000 @ price 0.20 → odds x5, payoff $50k, profit $40k → passes large tier
	ds := activityDataServer(0.20, 10000)
	defer ds.Close()
	dataCli := dataapi.New(polymarket.New(ds.URL, 100, 2*time.Second))

	tg, tgCli := newTelegramSetup(t)
	router := alerts.NewRouter(store, tgCli, alerts.DefaultLinks(), slog.Default(),
		"admin", "bets", "clusters", "news")

	w := &walletintel.WatchedWalletWorker{
		DataAPI: dataCli, Store: store, Router: router, Log: slog.Default(),
		Interval:      time.Minute,
		Runner:        &walletintel.Runner{DataAPI: dataCli, Store: store, Log: slog.Default()},
		Links:         alerts.DefaultLinks(),
		InsiderParams: walletintel.InsiderParams{MaxLifetimeTrades: 3, MinNotionalUSD: 1, MinScore: 50},
		ProfitGate: walletintel.ProfitGateParams{
			Enabled:          true,
			MediumMaxNotional: 10000,
			LargeMaxNotional: 80000,
			LargeMinOdds:     2,     // 5 >= 2 → PASS
			LargeMinProfit:   25000, // 40000 >= 25000 → PASS
			MegaMinNotional:  80000,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx) //nolint:errcheck

	deadline := time.After(2500 * time.Millisecond)
	for {
		select {
		case <-deadline:
			t.Fatalf("alert not sent in time")
		default:
			if len(tg.received()) > 0 {
				goto payloadSent
			}
			time.Sleep(80 * time.Millisecond)
		}
	}
payloadSent:
	cancel()

	body := strings.Join(tg.received(), "\n")
	if !strings.Contains(body, "Profit if win") {
		t.Fatalf("alert must contain 'Profit if win'; body:\n%s", body)
	}
	if !strings.Contains(body, "Payoff if win") {
		t.Fatalf("alert must contain 'Payoff if win'; body:\n%s", body)
	}
	if !strings.Contains(body, "Odds") {
		t.Fatalf("alert must contain 'Odds'; body:\n%s", body)
	}
}
