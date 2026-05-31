package walletintel

import (
	"testing"
	"time"
)

func mkBet(id, wallet, market string, dir Direction, when time.Time, notional, price float64, walletClass string, score int) WatchedBet {
	return WatchedBet{
		ID:          id,
		WalletID:    wallet,
		WalletClass: walletClass,
		WalletScore: score,
		MarketID:    market,
		Direction:   dir,
		Notional:    notional,
		Price:       price,
		Timestamp:   when,
	}
}

func defaultClusterParams() ClusterParams {
	return ClusterParams{
		WindowBefore:     3 * time.Hour,
		WindowAfter:      3 * time.Hour,
		MinWallets:       2,
		MinTotalNotional: 5000,
		MinQualityScore:  0,
	}
}

func TestCluster_DetectsAlignedBetsWithinWindow(t *testing.T) {
	now := time.Now().UTC()
	bets := []WatchedBet{
		mkBet("a", "w1", "M1", DirYesBuy, now, 10000, 0.3, "shark", 75),
		mkBet("b", "w2", "M1", DirYesBuy, now.Add(30*time.Minute), 12000, 0.32, "insider_like", 80),
		mkBet("c", "w3", "M1", DirYesBuy, now.Add(2*time.Hour), 15000, 0.33, "shark", 70),
	}
	out := FindClusters(bets, defaultClusterParams())
	if len(out) == 0 {
		t.Fatalf("expected at least one cluster")
	}
	cl := out[0]
	if cl.WalletCount != 3 {
		t.Fatalf("expected 3 wallets, got %d", cl.WalletCount)
	}
	if cl.TotalNotional < 37000 {
		t.Fatalf("expected total notional ~37000 got %v", cl.TotalNotional)
	}
	if cl.Direction != DirYesBuy {
		t.Fatalf("expected DirYesBuy got %v", cl.Direction)
	}
}

func TestCluster_OutsideWindowDoesNotCluster(t *testing.T) {
	now := time.Now().UTC()
	bets := []WatchedBet{
		mkBet("a", "w1", "M1", DirYesBuy, now, 10000, 0.3, "shark", 75),
		mkBet("b", "w2", "M1", DirYesBuy, now.Add(24*time.Hour), 10000, 0.3, "shark", 75),
	}
	out := FindClusters(bets, defaultClusterParams())
	if len(out) != 0 {
		t.Fatalf("expected no cluster across 24h, got %d", len(out))
	}
}

func TestCluster_DirectionMismatchDoesNotCluster(t *testing.T) {
	now := time.Now().UTC()
	bets := []WatchedBet{
		mkBet("a", "w1", "M1", DirYesBuy, now, 10000, 0.3, "shark", 75),
		mkBet("b", "w2", "M1", DirNoBuy, now.Add(time.Hour), 10000, 0.3, "shark", 75),
	}
	out := FindClusters(bets, defaultClusterParams())
	if len(out) != 0 {
		t.Fatalf("expected no cluster across opposite directions, got %d", len(out))
	}
}

func TestCluster_BelowMinWalletsIgnored(t *testing.T) {
	now := time.Now().UTC()
	bets := []WatchedBet{
		mkBet("a", "w1", "M1", DirYesBuy, now, 100000, 0.3, "shark", 75),
		mkBet("b", "w1", "M1", DirYesBuy, now.Add(time.Hour), 100000, 0.3, "shark", 75), // same wallet
	}
	out := FindClusters(bets, defaultClusterParams())
	if len(out) != 0 {
		t.Fatalf("expected no cluster from single wallet")
	}
}

func TestCluster_BelowMinNotionalIgnored(t *testing.T) {
	now := time.Now().UTC()
	bets := []WatchedBet{
		mkBet("a", "w1", "M1", DirYesBuy, now, 100, 0.3, "shark", 75),
		mkBet("b", "w2", "M1", DirYesBuy, now.Add(time.Hour), 100, 0.3, "shark", 75),
	}
	p := defaultClusterParams()
	p.MinTotalNotional = 5000
	out := FindClusters(bets, p)
	if len(out) != 0 {
		t.Fatalf("expected no cluster below min notional")
	}
}

func TestCluster_DedupKeyStableAcrossOrder(t *testing.T) {
	now := time.Now().UTC()
	bets1 := []WatchedBet{
		mkBet("a", "w1", "M1", DirYesBuy, now, 10000, 0.3, "shark", 75),
		mkBet("b", "w2", "M1", DirYesBuy, now.Add(30*time.Minute), 12000, 0.32, "shark", 75),
	}
	bets2 := []WatchedBet{
		mkBet("b", "w2", "M1", DirYesBuy, now.Add(30*time.Minute), 12000, 0.32, "shark", 75),
		mkBet("a", "w1", "M1", DirYesBuy, now, 10000, 0.3, "shark", 75),
	}
	o1 := FindClusters(bets1, defaultClusterParams())
	o2 := FindClusters(bets2, defaultClusterParams())
	if len(o1) == 0 || len(o2) == 0 {
		t.Fatalf("expected clusters in both")
	}
	if o1[0].DedupKey != o2[0].DedupKey {
		t.Fatalf("dedup key not stable: %q vs %q", o1[0].DedupKey, o2[0].DedupKey)
	}
}
