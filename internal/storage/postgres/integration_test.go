//go:build integration

// Run with: INTEGRATION_DB_URL=postgres://... go test -tags=integration ./internal/storage/postgres/...
//
// Spins up a clean schema in the configured Postgres, exercises every
// repository, and asserts idempotency / dedup invariants.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("INTEGRATION_DB_URL")
	if dsn == "" {
		t.Skip("INTEGRATION_DB_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Drop & recreate test schema to make tests deterministic. Uses a
	// dedicated schema_name `watchtower_test` if integration_schema=clean.
	// Simpler approach: drop all tables we own, then re-run migrations.
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
	migDir := filepath.Join("..", "..", "..", "migrations")
	if err := s.Migrate(ctx, migDir); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func TestIntegration_MigrationsApplyCleanly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tables := []string{
		"events", "markets", "market_tokens", "market_tag_links", "market_state",
		"wallets", "wallet_scores", "wallet_watchlist", "holder_snapshots",
		"wallet_trades", "watched_bets", "bet_clusters", "news_items",
		"alert_decisions", "telegram_deliveries", "market_price_samples",
	}
	for _, tbl := range tables {
		var n int
		if err := s.Pool.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.tables WHERE table_name=$1`,
			tbl).Scan(&n); err != nil {
			t.Fatalf("query %s: %v", tbl, err)
		}
		if n != 1 {
			t.Fatalf("table %s not present after migrate", tbl)
		}
	}
}

func TestIntegration_EventMarketTokenUpsertIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	eid, err := s.UpsertEvent(ctx, Event{Slug: "ev1", Title: "Event 1", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	eid2, err := s.UpsertEvent(ctx, Event{Slug: "ev1", Title: "Event 1 updated", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if eid != eid2 {
		t.Fatalf("event upsert created new row: %s vs %s", eid, eid2)
	}
	mid, err := s.UpsertMarket(ctx, Market{
		ConditionID: "0xcond", EventID: eid, Slug: "m1", Question: "Q?",
		Active: true, Volume: 1234.5, NegRisk: true, UMAResolutionStatus: "proposed",
	})
	if err != nil {
		t.Fatal(err)
	}
	mid2, err := s.UpsertMarket(ctx, Market{
		ConditionID: "0xcond", EventID: eid, Slug: "m1", Question: "Q?",
		Active: true, Volume: 2222, NegRisk: true, UMAResolutionStatus: "resolved",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mid != mid2 {
		t.Fatalf("market upsert created new row")
	}
	if err := s.UpsertMarketToken(ctx, mid, MarketToken{OutcomeIndex: 0, OutcomeName: "Yes", ClobTokenID: "tok1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertMarketToken(ctx, mid, MarketToken{OutcomeIndex: 0, OutcomeName: "Yes", ClobTokenID: "tok1"}); err != nil {
		t.Fatal(err) // idempotent
	}
	// verify market got the latest fields
	row, err := s.GetMarketByConditionID(ctx, "0xcond")
	if err != nil {
		t.Fatal(err)
	}
	if row.UMAResolutionStatus != "resolved" || row.Volume != 2222 || !row.NegRisk {
		t.Fatalf("upsert did not refresh: %+v", row)
	}
}

func TestIntegration_WalletScoresAndWatchlist(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wid, err := s.UpsertWallet(ctx, Wallet{ProxyWallet: "0xabc", Pseudonym: "P"})
	if err != nil {
		t.Fatal(err)
	}
	snap := map[string]any{"sample_size_score": 12}
	id, err := s.InsertScore(ctx, wid, ScoreRow{
		Strategy: "shark_score", Class: "shark", Score: 85,
		Confidence: 0.8, Promote: true, ScoreVersion: "v1.0.0",
		FeatureSnapshot: snap,
		ReasonCodes:     []string{"HIGH_SAMPLE_SIZE"},
		MissingData:     []string{},
	})
	if err != nil || id == "" {
		t.Fatalf("score insert: id=%s err=%v", id, err)
	}
	// upsert watchlist twice
	for i, score := range []int{85, 90} {
		if err := s.UpsertWatchlist(ctx, WatchlistRow{
			WalletID: wid, Class: "shark", Status: "active",
			Score: score, Confidence: 0.85,
			ReasonCodes:     []string{"HIGH_SAMPLE_SIZE"},
			FeatureSnapshot: snap, ScoreVersion: "v1.0.0",
		}); err != nil {
			t.Fatalf("upsert watchlist #%d: %v", i, err)
		}
	}
	wallets, err := s.ListActiveWatchlist(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(wallets) != 1 || wallets[0].Score != 90 {
		t.Fatalf("expected single row with score 90, got %+v", wallets)
	}
}

func TestIntegration_WalletTradesDedup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wid, _ := s.UpsertWallet(ctx, Wallet{ProxyWallet: "0xtdedup"})
	trade := WalletTrade{
		TransactionHash: "0xtx1", WalletID: wid,
		ConditionID: "0xC", Outcome: "Yes", Side: "BUY",
		Direction: "YES_BUY", Price: 0.3, Size: 100, UsdcSize: 30,
		Timestamp: time.Now(), Source: "test",
	}
	id1, inserted1, err := s.InsertWalletTrade(ctx, trade)
	if err != nil || !inserted1 {
		t.Fatalf("first insert err=%v inserted=%v", err, inserted1)
	}
	id2, inserted2, err := s.InsertWalletTrade(ctx, trade)
	if err != nil {
		t.Fatal(err)
	}
	if inserted2 {
		t.Fatalf("expected dedup on second insert")
	}
	if id1 != id2 {
		t.Fatalf("dedup id mismatch: %s vs %s", id1, id2)
	}
}

func TestIntegration_WatchedBetsAndCluster(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	eid, _ := s.UpsertEvent(ctx, Event{Slug: "ev2", Title: "E", Active: true})
	mid, _ := s.UpsertMarket(ctx, Market{ConditionID: "0xc2", EventID: eid, Slug: "m2", Active: true})
	wid, _ := s.UpsertWallet(ctx, Wallet{ProxyWallet: "0xw2"})
	tradeID, _, err := s.InsertWalletTrade(ctx, WalletTrade{
		TransactionHash: "0xt2", WalletID: wid, MarketID: mid,
		ConditionID: "0xc2", Outcome: "Yes", Side: "BUY", Direction: "YES_BUY",
		Price: 0.3, Size: 100, Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.InsertWatchedBet(ctx, WatchedBet{
		WalletTradeID: tradeID, WalletID: wid, MarketID: mid,
		Direction: "YES_BUY", Notional: 10000, Price: 0.3, Odds: 3.33, PayoffIfWin: 33333,
		WalletClass: "shark", WalletScore: 80, FeatureSnapshot: map[string]any{},
		DetectedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	recent, err := s.ListRecentWatchedBets(ctx, time.Now().Add(-1*time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 {
		t.Fatalf("expected 1 watched bet, got %d", len(recent))
	}
	// cluster upsert idempotent
	row := ClusterRow{
		MarketID: mid, Direction: "YES_BUY",
		WindowStart: time.Now().Add(-3 * time.Hour), WindowEnd: time.Now(),
		WalletCount: 2, TotalNotional: 20000,
		WeightedPrice: 0.3, AverageOdds: 3.33, PayoffIfWinTotal: 66666,
		ClusterScore: 80, DedupKey: "dk-1",
		FeatureSnapshot: map[string]any{"wallet_count": 2},
	}
	cid, inserted, err := s.UpsertCluster(ctx, row)
	if err != nil || !inserted {
		t.Fatalf("first upsert err=%v inserted=%v", err, inserted)
	}
	row.WalletCount = 3
	row.TotalNotional = 30000
	cid2, inserted2, err := s.UpsertCluster(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if cid != cid2 {
		t.Fatalf("cluster dedup id mismatch: %s vs %s", cid, cid2)
	}
	if inserted2 {
		t.Fatalf("second upsert must not be inserted_new")
	}
}

func TestIntegration_AlertDecisionsAndDeliveries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	dec := AlertDecision{
		AlertType: "SHARK_BET", EntityType: "watched_bet", EntityID: "x",
		Severity: "WARNING", ShouldSend: true,
		UserAlertAllowed: true, AdminAlertAllowed: true,
		FeatureSnapshot: map[string]any{"k": "v"}, DedupKey: "dedup-1",
	}
	id, isNew, err := s.InsertAlertDecision(ctx, dec)
	if err != nil || !isNew {
		t.Fatalf("first insert err=%v isNew=%v", err, isNew)
	}
	id2, isNew2, err := s.InsertAlertDecision(ctx, dec)
	if err != nil {
		t.Fatal(err)
	}
	if isNew2 || id != id2 {
		t.Fatalf("dedup mismatch: id=%s id2=%s isNew2=%v", id, id2, isNew2)
	}
	if err := s.InsertTelegramDelivery(ctx, TelegramDelivery{
		AlertDecisionID: id, ChatID: "100", Status: "failed",
		Error: "boom", Body: "hello", Attempt: 1, NextAttemptAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	due, err := s.ListFailedDeliveriesDue(ctx, 5, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 failed due, got %d", len(due))
	}
	if err := s.MarkDeliveryRetry(ctx, due[0].DeliveryID, time.Now().Add(time.Minute), "still failing"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDeliverySucceeded(ctx, due[0].DeliveryID, "msg-42"); err != nil {
		t.Fatal(err)
	}
	ok, _ := s.HasSuccessfulDelivery(ctx, id)
	if !ok {
		t.Fatalf("expected successful delivery flag")
	}
}

func TestIntegration_NewsDedup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	n := NewsItem{
		EventSlug: "ev3", Title: "News", Summary: "S", SourceURL: "u",
		NewsTimestamp: time.Now(), Fingerprint: "fp-1",
	}
	id1, ins1, err := s.InsertNews(ctx, n)
	if err != nil || !ins1 {
		t.Fatalf("first err=%v ins=%v", err, ins1)
	}
	id2, ins2, err := s.InsertNews(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if ins2 || id1 != id2 {
		t.Fatalf("news dedup mismatch")
	}
}

func TestIntegration_HolderSnapshotsByMarketTime(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	eid, _ := s.UpsertEvent(ctx, Event{Slug: "ev-h", Title: "E", Active: true})
	mid, _ := s.UpsertMarket(ctx, Market{ConditionID: "0xH", EventID: eid, Slug: "mh", Active: true})
	wid, _ := s.UpsertWallet(ctx, Wallet{ProxyWallet: "0xwh"})
	if err := s.InsertHolderSnapshot(ctx, HolderSnapshot{
		MarketID: mid, WalletID: wid, OutcomeIndex: 0,
		Amount: 1000, Rank: 1, Source: "test", SnapshotAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM holder_snapshots WHERE market_id=$1::uuid`, mid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 snapshot, got %d", n)
	}
}

func TestIntegration_PriceSamplesAndNearest(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	eid, _ := s.UpsertEvent(ctx, Event{Slug: "ev-ps", Title: "E", Active: true})
	mid, _ := s.UpsertMarket(ctx, Market{ConditionID: "0xPS", EventID: eid, Slug: "mps", Active: true})
	t0 := time.Now()
	for i := 0; i < 5; i++ {
		_ = s.InsertPriceSample(ctx, PriceSample{
			MarketID: mid, Outcome: "Yes",
			Price: 0.30 + float64(i)*0.01, Midpoint: 0.30 + float64(i)*0.01,
			SampledAt: t0.Add(time.Duration(i) * time.Minute), Source: "test",
		})
	}
	p, when, err := s.NearestSampleAfter(ctx, mid, "Yes", t0.Add(90*time.Second))
	if err != nil {
		t.Fatalf("nearest: %v", err)
	}
	if p < 0.30 || p > 0.36 {
		t.Fatalf("unexpected price %v", p)
	}
	if when.Before(t0.Add(90 * time.Second)) {
		t.Fatalf("nearest before query time: %v", when)
	}
}

func TestIntegration_FeatureSnapshotRoundtrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wid, _ := s.UpsertWallet(ctx, Wallet{ProxyWallet: "0xfs"})
	snap := map[string]any{
		"sample_size_score": 12,
		"reasons":           []string{"A", "B"},
		"nested":            map[string]any{"x": 1.5},
	}
	if _, err := s.InsertScore(ctx, wid, ScoreRow{
		Strategy: "test", Class: "x", Score: 1, Confidence: 0.5,
		Promote: false, ScoreVersion: "v1", FeatureSnapshot: snap,
	}); err != nil {
		t.Fatal(err)
	}
	var jsonStr string
	if err := s.Pool.QueryRow(ctx,
		`SELECT feature_snapshot::text FROM wallet_scores WHERE wallet_id=$1::uuid`, wid).Scan(&jsonStr); err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &back); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if int(back["sample_size_score"].(float64)) != 12 {
		t.Fatalf("snap value lost: %+v", back)
	}
}

// helper used by some assertions
func MustEqual[T comparable](t *testing.T, got, want T, msg string) {
	if got != want {
		t.Fatalf("%s: got %v want %v", msg, got, want)
	}
}

var _ = fmt.Sprintf
