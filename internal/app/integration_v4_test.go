//go:build integration

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
	"github.com/Borislavv/polymarket-sharks/internal/telegram"
	"github.com/Borislavv/polymarket-sharks/internal/walletintel"
)

// fakeV4DataAPIServer responds with a Polymarket-like profile that satisfies
// v4 historical-shark gates: many closed positions, positive ROI, win-rate
// above 0.75, average closed stake above $10k.
func fakeV4SharkAPI(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/positions", func(w http.ResponseWriter, r *http.Request) {
		// 40 closed positions, 32 wins, 8 losses (WR 0.8), avg stake $15k, ROI ~0.4
		var arr []map[string]any
		for i := 0; i < 32; i++ {
			arr = append(arr, map[string]any{
				"proxyWallet":  "0xshark",
				"conditionId":  fmt.Sprintf("0xC%d", i),
				"outcome":      "Yes",
				"size":         0,
				"totalBought":  15000.0,
				"realizedPnl":  7500.0 + float64(i)*100, // positive
				"avgPrice":     0.40,
				"currentValue": 0,
			})
		}
		for i := 0; i < 8; i++ {
			arr = append(arr, map[string]any{
				"proxyWallet":  "0xshark",
				"conditionId":  fmt.Sprintf("0xL%d", i),
				"outcome":      "No",
				"size":         0,
				"totalBought":  15000.0,
				"realizedPnl":  -3000.0 - float64(i)*100,
				"avgPrice":     0.45,
				"currentValue": 0,
			})
		}
		b, _ := json.Marshal(arr)
		w.Write(b)
	})
	mux.HandleFunc("/trades", func(w http.ResponseWriter, r *http.Request) {
		// short pagination so backfill completes quickly
		offset := 0
		fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
		if offset > 0 {
			w.Write([]byte(`[]`))
			return
		}
		var arr []map[string]any
		for i := 0; i < 50; i++ {
			arr = append(arr, map[string]any{
				"transactionHash": fmt.Sprintf("0xt%d", i),
				"proxyWallet":     "0xshark",
				"conditionId":     fmt.Sprintf("0xC%d", i),
				"asset":           "tok-yes",
				"side":            "BUY",
				"outcome":         "Yes",
				"price":           0.40,
				"size":            37500,
				"usdcSize":        15000,
				"timestamp":       time.Now().Add(-time.Duration(i) * time.Hour).Unix(),
			})
		}
		b, _ := json.Marshal(arr)
		w.Write(b)
	})
	mux.HandleFunc("/traded", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user":"0xshark","traded":40}`))
	})
	mux.HandleFunc("/value", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"user":"0xshark","value":250000}]`))
	})
	mux.HandleFunc("/activity", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fmt.Sprintf(`[{
			"transactionHash":"0xnewbet1","proxyWallet":"0xshark","asset":"tok-yes",
			"conditionId":"0xCID-NEW","side":"BUY","outcome":"Yes","price":0.30,
			"size":50000,"usdcSize":15000,"timestamp":%d,"type":"TRADE","eventSlug":"e-test","title":"Q?"
		}]`, time.Now().Unix())))
	})
	return httptest.NewServer(mux)
}

// fakeV4InsiderAPI returns a new wallet with 1 lifetime trade and a single
// $25k bet at price 0.20 (odds 5x) in a war/politics category.
func fakeV4InsiderAPI(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/positions", func(w http.ResponseWriter, r *http.Request) {
		// no closed positions yet — clean streak
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/trades", func(w http.ResponseWriter, r *http.Request) {
		offset := 0
		fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
		if offset > 0 {
			w.Write([]byte(`[]`))
			return
		}
		w.Write([]byte(fmt.Sprintf(`[{
			"transactionHash":"0xtrade1","proxyWallet":"0xinsider","asset":"tok-yes",
			"conditionId":"0xCID-INS","side":"BUY","outcome":"Yes","price":0.20,
			"size":125000,"usdcSize":25000,"timestamp":%d
		}]`, time.Now().Add(-1*time.Hour).Unix())))
	})
	mux.HandleFunc("/traded", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user":"0xinsider","traded":1}`))
	})
	mux.HandleFunc("/value", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"user":"0xinsider","value":25000}]`))
	})
	mux.HandleFunc("/activity", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fmt.Sprintf(`[{
			"transactionHash":"0xnewbet","proxyWallet":"0xinsider","asset":"tok-yes",
			"conditionId":"0xCID-INS","side":"BUY","outcome":"Yes","price":0.20,
			"size":125000,"usdcSize":25000,"timestamp":%d,"type":"TRADE","eventSlug":"e-war","title":"Q?"
		}]`, time.Now().Unix())))
	})
	return httptest.NewServer(mux)
}

func TestV4_HistoricalSharkPromotes(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	ds := fakeV4SharkAPI(t)
	defer ds.Close()
	dataCli := dataapi.New(polymarket.New(ds.URL, 100, 2*time.Second))

	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xshark"})
	// seed candidate evidence so the backfill worker picks the wallet up
	_ = store.InsertCandidateEvidence(context.Background(), postgres.CandidateEvidence{
		WalletID:    wid,
		Source:      "positions_by_volume",
		SourceRank:  1,
		TotalBought: 600000,
		CashPnL:     300000,
		RealizedPnL: 220000,
	})

	bf := &walletintel.HistoryBackfillWorker{
		DataAPI:        dataCli,
		Store:          store,
		Log:            slog.Default(),
		BatchSize:      10,
		Concurrency:    1,
		ClosedPageSize: 500,
		TradePageSize:  500,
		MaxClosedPages: 5,
		MaxTradePages:  5,
	}
	if _, err := bf.BackfillOne(context.Background(), wid, "0xshark"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	runner := &walletintel.Runner{
		DataAPI: dataCli, Store: store, Log: slog.Default(),
		SharkParams: walletintel.SharkParams{
			HistMinClosedPositions: 25,
			HistMinROI:             0.3333,
			HistMinWinRate:         0.75,
			HistMinAvgStakeUSD:     10_000,
		},
		InsiderParams: walletintel.InsiderParams{
			MaxLifetimeForCapture: 10, MinNotionalUSD: 20_000, MinOdds: 3.0,
		},
	}
	dec, err := runner.ScoreWallet(context.Background(), wid, "0xshark", nil, walletintel.MarketRulesInputs{}, false, 0)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if dec.FinalClass != "shark" || !dec.Promote {
		t.Fatalf("expected shark promotion, got class=%s promote=%v reasons=%v", dec.FinalClass, dec.Promote, dec.ReasonCodes)
	}
	stats, err := store.GetHistoricalCloseStats(context.Background(), wid)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.ClosedCount < 25 {
		t.Fatalf("expected at least 25 closed positions, got %d", stats.ClosedCount)
	}
	if stats.WinRate <= 0.75 {
		t.Fatalf("expected win-rate > 0.75, got %v", stats.WinRate)
	}
}

func TestV4_OpenPositionsAloneCannotPromote(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	// Build a fake API where every /positions row has size>0 AND realized_pnl=0
	// → IsClosed=false for all. The backfill records zero closed positions.
	mux := http.NewServeMux()
	mux.HandleFunc("/positions", func(w http.ResponseWriter, r *http.Request) {
		var arr []map[string]any
		for i := 0; i < 30; i++ {
			arr = append(arr, map[string]any{
				"proxyWallet":  "0xopen",
				"conditionId":  fmt.Sprintf("0xCO%d", i),
				"outcome":      "Yes",
				"size":         100, // still open
				"totalBought":  25000.0,
				"realizedPnl":  0, // nothing realized yet
				"avgPrice":     0.40,
				"currentValue": 30000,
			})
		}
		b, _ := json.Marshal(arr)
		w.Write(b)
	})
	mux.HandleFunc("/trades", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/traded", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user":"0xopen","traded":30}`))
	})
	mux.HandleFunc("/value", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"user":"0xopen","value":900000}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dataCli := dataapi.New(polymarket.New(srv.URL, 100, 2*time.Second))

	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xopen"})
	_ = store.InsertCandidateEvidence(context.Background(), postgres.CandidateEvidence{
		WalletID: wid, Source: "positions_by_volume", SourceRank: 1, TotalBought: 750000,
	})

	bf := &walletintel.HistoryBackfillWorker{
		DataAPI: dataCli, Store: store, Log: slog.Default(),
		ClosedPageSize: 500, TradePageSize: 500,
		MaxClosedPages: 5, MaxTradePages: 5,
	}
	if _, err := bf.BackfillOne(context.Background(), wid, "0xopen"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	runner := &walletintel.Runner{
		DataAPI: dataCli, Store: store, Log: slog.Default(),
		SharkParams: walletintel.SharkParams{
			HistMinClosedPositions: 25, HistMinROI: 0.3333,
			HistMinWinRate: 0.75, HistMinAvgStakeUSD: 10_000,
		},
		InsiderParams: walletintel.InsiderParams{MaxLifetimeForCapture: 10, MinNotionalUSD: 20_000, MinOdds: 3.0},
	}
	dec, _ := runner.ScoreWallet(context.Background(), wid, "0xopen", nil, walletintel.MarketRulesInputs{}, false, 0)
	if dec.FinalClass == "shark" {
		t.Fatalf("open positions alone must not promote shark, got %+v", dec)
	}
}

type rwt struct{ base string }

func (r rwt) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = r.base[len("http://"):]
	return http.DefaultTransport.RoundTrip(req)
}

func TestV4_InsiderFirstBigBetAlertCreated(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	eid, _ := store.UpsertEvent(context.Background(), postgres.Event{Slug: "e-war", Title: "War 2026", Category: "war", Active: true})
	_, _ = store.UpsertMarket(context.Background(), postgres.Market{ConditionID: "0xCID-INS", EventID: eid, Slug: "m-war", Question: "Will war?", Active: true})

	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xinsider"})
	_ = store.UpsertWatchlist(context.Background(), postgres.WatchlistRow{
		WalletID: wid, Class: "insider_like", Status: "active", Score: 70, Confidence: 0.7,
		FeatureSnapshot: map[string]any{}, ScoreVersion: walletintel.ScoreVersion,
	})

	ds := fakeV4InsiderAPI(t)
	defer ds.Close()
	dataCli := dataapi.New(polymarket.New(ds.URL, 100, 2*time.Second))

	tg := &fakeTelegram{}
	tgSrv := httptest.NewServer(tg.handler())
	defer tgSrv.Close()
	tgCli := telegram.New("test-token", 100, time.Second)
	tgCli.HTTP = &http.Client{Transport: rwt{base: tgSrv.URL}, Timeout: time.Second}

	router := alerts.NewRouter(store, tgCli, alerts.DefaultLinks(), slog.Default(),
		"admin", "bets", "clusters", "news")

	runner := &walletintel.Runner{
		DataAPI: dataCli, Store: store, Log: slog.Default(),
		InsiderParams: walletintel.InsiderParams{
			MinNotionalUSD: 20_000, MinOdds: 3.0, MaxLifetimeForCapture: 10,
			HighImpactCategories: []string{"war", "politics"},
		},
	}
	w := &walletintel.WatchedWalletWorker{
		DataAPI: dataCli, Store: store, Router: router, Log: slog.Default(),
		Interval: time.Minute, Runner: runner, Links: alerts.DefaultLinks(),
		InsiderParams: runner.InsiderParams,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx) //nolint:errcheck
	deadline := time.After(2 * time.Second)
	var calls atomic.Int32
	for {
		select {
		case <-deadline:
			t.Fatalf("insider alert not created in time. tg received=%v calls=%d", tg.received(), calls.Load())
		default:
			calls.Add(1)
			var n int
			store.Pool.QueryRow(context.Background(),
				`SELECT count(*) FROM alert_decisions WHERE alert_type LIKE 'INSIDER_LIKE_%'`).Scan(&n)
			if n > 0 {
				cancel()
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func TestV4_WatchedWorker_IgnoresStreakBroken(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()
	eid, _ := store.UpsertEvent(context.Background(), postgres.Event{Slug: "e-war", Title: "T", Active: true})
	_, _ = store.UpsertMarket(context.Background(), postgres.Market{ConditionID: "0xCID-INS", EventID: eid, Slug: "m", Active: true})
	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xbroken"})
	_ = store.UpsertWatchlist(context.Background(), postgres.WatchlistRow{
		WalletID: wid, Class: "insider_like", Status: "streak_broken", Score: 50, Confidence: 0.5,
		FeatureSnapshot: map[string]any{}, ScoreVersion: walletintel.ScoreVersion,
	})
	got, _ := store.ListActiveWatchlist(context.Background())
	for _, w := range got {
		if w.ProxyWallet == "0xbroken" {
			t.Fatalf("ListActiveWatchlist must skip streak_broken")
		}
	}
}
