//go:build integration

package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
	"github.com/Borislavv/polymarket-sharks/internal/walletintel"
)

func TestEvidence_TopHolderWithoutProfitCannotAutoPromote(t *testing.T) {
	store := newTestStore(t)
	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xtop"})
	// Holder-only evidence: large size, NO profit data → HasProfitEvidence=false.
	_ = store.InsertCandidateEvidence(context.Background(), postgres.CandidateEvidence{
		WalletID: wid, Source: postgres.EvidenceSourceHolders, SourceRank: 1,
		HolderAmount: 1_000_000,
	})
	ok, err := store.HasProfitEvidence(context.Background(), wid)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("HasProfitEvidence should be false for holder-only row")
	}
}

func TestEvidence_PositiveProfitFlagDetected(t *testing.T) {
	store := newTestStore(t)
	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xpnl"})
	_ = store.InsertCandidateEvidence(context.Background(), postgres.CandidateEvidence{
		WalletID: wid, Source: postgres.EvidenceSourcePositionsPNL, SourceRank: 1,
		CashPnL: 25_000, CurrentValue: 100_000,
	})
	ok, _ := store.HasProfitEvidence(context.Background(), wid)
	if !ok {
		t.Fatalf("expected HasProfitEvidence=true for cash_pnl>0")
	}
}

func TestReconcileFailed_DemotesUnderlyingShark(t *testing.T) {
	store := newTestStore(t)
	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xfail"})
	// Watchlist with arbitration snapshot (does NOT contain scoring keys).
	_, err := store.Pool.Exec(context.Background(), `
		INSERT INTO wallet_watchlist (wallet_id, class, status, score, confidence,
		    reason_codes, feature_snapshot, score_version, promoted_at, updated_at)
		VALUES ($1::uuid, 'shark', 'active', 70, 0.8, '{}', '{}'::jsonb, $2, now(), now())
	`, wid, walletintel.ScoreVersion)
	if err != nil {
		t.Fatal(err)
	}
	// wallet_scores: promote=false — the source of truth for the new reconcile.
	failSnap, _ := json.Marshal(map[string]any{
		"closed_positions_count": 24, "win_rate": 0.7, "roi": 0.25,
		"closed_positions_complete": true,
	})
	_, _ = store.Pool.Exec(context.Background(), `
		INSERT INTO wallet_scores (wallet_id, strategy, class, score, confidence, promote,
		    score_version, feature_snapshot, reason_codes)
		VALUES ($1::uuid, 'shark_score', 'shark', 30, 0.4, false, $2, $3::jsonb,
		        ARRAY['ROI_TOO_LOW','WIN_RATE_TOO_LOW'])
	`, wid, walletintel.ScoreVersion, string(failSnap))
	n, err := walletintel.ReconcileFailedSharks(context.Background(), store, walletintel.SharkParams{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 demotion, got %d", n)
	}
	var status, class string
	store.Pool.QueryRow(context.Background(),
		`SELECT status, class FROM wallet_watchlist WHERE wallet_id=$1::uuid`, wid).Scan(&status, &class)
	if status != "unactual" || class != "rejected_unactual" {
		t.Fatalf("expected unactual/rejected_unactual, got %s/%s", status, class)
	}
}

func TestReconcileFailed_KeepsValidHistoricalShark(t *testing.T) {
	store := newTestStore(t)
	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xvalid"})
	// Watchlist with arbitration snapshot (does NOT contain scoring keys — matches
	// the real production scenario where arbitration snapshot is stored).
	_, _ = store.Pool.Exec(context.Background(), `
		INSERT INTO wallet_watchlist (wallet_id, class, status, score, confidence,
		    reason_codes, feature_snapshot, score_version, promoted_at, updated_at)
		VALUES ($1::uuid, 'shark', 'active', 80, 0.9, '{}', '{}'::jsonb, $2, now(), now())
	`, wid, walletintel.ScoreVersion)
	// wallet_scores: promote=true — source of truth for reconcile.
	validSnap, _ := json.Marshal(map[string]any{
		"closed_positions_count": 40, "win_rate": 0.80, "roi": 0.45,
		"avg_closed_position_stake": 18_000.0, "realized_pnl": 180_000.0,
		"closed_positions_complete": true,
	})
	_, _ = store.Pool.Exec(context.Background(), `
		INSERT INTO wallet_scores (wallet_id, strategy, class, score, confidence, promote,
		    score_version, feature_snapshot, reason_codes)
		VALUES ($1::uuid, 'shark_score', 'shark', 80, 0.9, true, $2, $3::jsonb,
		        ARRAY['SHARK_HISTORICAL_EDGE'])
	`, wid, walletintel.ScoreVersion, string(validSnap))
	n, _ := walletintel.ReconcileFailedSharks(context.Background(), store, walletintel.SharkParams{}, nil)
	if n != 0 {
		t.Fatalf("valid v4 shark must not be demoted, got %d", n)
	}
	// Confirm still active.
	var status string
	_ = store.Pool.QueryRow(context.Background(),
		`SELECT status FROM wallet_watchlist WHERE wallet_id=$1::uuid`, wid).Scan(&status)
	if status != "active" {
		t.Fatalf("expected active, got %s", status)
	}
}

func TestWatchedWorker_IgnoresUnactual(t *testing.T) {
	store := newTestStore(t)
	wid, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xunact"})
	_, _ = store.Pool.Exec(context.Background(), `
		INSERT INTO wallet_watchlist (wallet_id, class, status, score, confidence,
		    reason_codes, feature_snapshot, score_version, promoted_at, updated_at)
		VALUES ($1::uuid, 'rejected_unactual', 'unactual', 0, 0, ARRAY['DEMOTED_UNACTUAL'],
		        '{}'::jsonb, 'v0', now(), now())
	`, wid)
	rows, err := store.ListActiveWatchlist(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == wid {
			t.Fatalf("ListActiveWatchlist must skip unactual rows")
		}
	}
}

func TestBurst_FilteredByActiveCurrentScoreVersion(t *testing.T) {
	store := newTestStore(t)
	// seed two wallets: one active+current, one unactual; both with 3 entries in last 5m
	w1, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xact"})
	w2, _ := store.UpsertWallet(context.Background(), postgres.Wallet{ProxyWallet: "0xunact"})
	_, _ = store.Pool.Exec(context.Background(), `
		INSERT INTO wallet_watchlist (wallet_id, class, status, score, confidence,
		    reason_codes, feature_snapshot, score_version, promoted_at, updated_at)
		VALUES ($1::uuid, 'shark', 'active', 80, 0.9, '{}', '{}'::jsonb, $2, now(), now())
	`, w1, walletintel.ScoreVersion)
	_, _ = store.Pool.Exec(context.Background(), `
		INSERT INTO wallet_watchlist (wallet_id, class, status, score, confidence,
		    reason_codes, feature_snapshot, score_version, promoted_at, updated_at)
		VALUES ($1::uuid, 'rejected_unactual', 'unactual', 0, 0, '{}', '{}'::jsonb, $2, now(), now())
	`, w2, walletintel.ScoreVersion)
	// seed market + 3 entry bets each
	eid, _ := store.UpsertEvent(context.Background(), postgres.Event{Slug: "ev-b3", Title: "B3", Active: true})
	mid, _ := store.UpsertMarket(context.Background(), postgres.Market{ConditionID: "0xb3", EventID: eid, Slug: "m-b3", Active: true})
	for _, wid := range []string{w1, w2} {
		for i := 0; i < 3; i++ {
			tradeID, _, _ := store.InsertWalletTrade(context.Background(), postgres.WalletTrade{
				TransactionHash: wid + "-bt-" + time.Now().Format("150405.000000000") + "-" + string('a'+rune(i)),
				WalletID:        wid, MarketID: mid, ConditionID: "0xb3",
				Outcome: "Yes", Side: "BUY", Direction: "YES_BUY",
				Price: 0.3, Size: 100000, UsdcSize: 30000, Timestamp: time.Now(),
			})
			_, _ = store.InsertWatchedBet(context.Background(), postgres.WatchedBet{
				WalletTradeID: tradeID, WalletID: wid, MarketID: mid, Direction: "YES_BUY",
				Notional: 30_000, Price: 0.3, Odds: 3.3, WalletClass: "shark",
				WalletScore: 80, FeatureSnapshot: map[string]any{}, DetectedAt: time.Now(),
				BetKind: "entry",
			})
		}
	}
	cands, err := store.FindBurstCandidates(context.Background(),
		time.Now(), 15*time.Minute, 3, 2, walletintel.ScoreVersion)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.WalletID == w2 {
			t.Fatalf("burst must NOT include unactual wallet")
		}
	}
	found := false
	for _, c := range cands {
		if c.WalletID == w1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected active wallet in burst candidates")
	}
}
