package walletintel

import (
	"testing"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

func tradeAt(side, outcome string, price, size float64, t time.Time, tx string) postgres.TradeForReplay {
	return postgres.TradeForReplay{
		TransactionHash: tx,
		ConditionID:     "0xC1",
		Outcome:         outcome,
		Side:            side,
		Price:           price,
		Size:            size,
		UsdcSize:        price * size,
		Timestamp:       t,
	}
}

func TestReconstruct_ProfitableExitBeforeOppositeResolution(t *testing.T) {
	// YES_BUY @0.20, YES_SELL @0.45 — realized cycle is profitable even if
	// the market later resolves NO. The reconstructor never reads market
	// outcome, so this is structurally guaranteed.
	now := time.Now()
	rows := ReconstructRealizedTrades("w-1", []postgres.TradeForReplay{
		tradeAt("BUY", "YES", 0.20, 100, now, "0xa"),
		tradeAt("SELL", "YES", 0.45, 100, now.Add(2*time.Hour), "0xb"),
	})
	if len(rows) != 1 {
		t.Fatalf("expected 1 realized cycle, got %d", len(rows))
	}
	got := rows[0]
	if got.RealizedPnL != 25 {
		t.Fatalf("expected pnl=25, got %v", got.RealizedPnL)
	}
	if got.RealizedROI != 25.0/(0.20*100) {
		t.Fatalf("unexpected ROI: %v", got.RealizedROI)
	}
	if got.Size != 100 {
		t.Fatalf("expected size=100, got %v", got.Size)
	}
}

func TestReconstruct_LosingTrade(t *testing.T) {
	now := time.Now()
	rows := ReconstructRealizedTrades("w-2", []postgres.TradeForReplay{
		tradeAt("BUY", "YES", 0.45, 100, now, "0xa"),
		tradeAt("SELL", "YES", 0.20, 100, now.Add(1*time.Hour), "0xb"),
	})
	if len(rows) != 1 || rows[0].RealizedPnL != -25 {
		t.Fatalf("expected single losing cycle pnl=-25, got %+v", rows)
	}
}

func TestReconstruct_PartialExit(t *testing.T) {
	now := time.Now()
	rows := ReconstructRealizedTrades("w-3", []postgres.TradeForReplay{
		tradeAt("BUY", "YES", 0.20, 100, now, "0xa"),
		tradeAt("SELL", "YES", 0.50, 40, now.Add(1*time.Hour), "0xb"),
	})
	if len(rows) != 1 {
		t.Fatalf("expected 1 partial-exit cycle, got %d", len(rows))
	}
	g := rows[0]
	if g.Size != 40 {
		t.Fatalf("expected realized size=40, got %v", g.Size)
	}
	// pnl = (0.50 - 0.20) * 40 = 12
	if g.RealizedPnL != 12 {
		t.Fatalf("expected pnl=12, got %v", g.RealizedPnL)
	}
}

func TestReconstruct_WeightedAverageOnMultipleBuys(t *testing.T) {
	now := time.Now()
	rows := ReconstructRealizedTrades("w-4", []postgres.TradeForReplay{
		tradeAt("BUY", "YES", 0.20, 100, now, "0xa"),
		tradeAt("BUY", "YES", 0.40, 100, now.Add(1*time.Hour), "0xb"),
		tradeAt("SELL", "YES", 0.50, 100, now.Add(2*time.Hour), "0xc"),
	})
	if len(rows) != 1 {
		t.Fatalf("expected 1 cycle, got %d", len(rows))
	}
	g := rows[0]
	// avg_entry = 0.30; pnl = (0.50 - 0.30) * 100 = 20
	if g.AvgEntryPrice != 0.30 {
		t.Fatalf("expected avg_entry=0.30, got %v", g.AvgEntryPrice)
	}
	if g.RealizedPnL != 20 {
		t.Fatalf("expected pnl=20, got %v", g.RealizedPnL)
	}
}

func TestReconstruct_UnresolvedOpenPositionDoesNotProduceCycle(t *testing.T) {
	now := time.Now()
	rows := ReconstructRealizedTrades("w-5", []postgres.TradeForReplay{
		tradeAt("BUY", "YES", 0.20, 100, now, "0xa"),
	})
	if len(rows) != 0 {
		t.Fatalf("open position must not produce a realized cycle: %+v", rows)
	}
}

func TestReconstruct_OrphanSellWithoutOpenPositionDropped(t *testing.T) {
	now := time.Now()
	rows := ReconstructRealizedTrades("w-6", []postgres.TradeForReplay{
		tradeAt("SELL", "YES", 0.50, 100, now, "0xa"),
	})
	if len(rows) != 0 {
		t.Fatalf("orphan SELL without open size must NOT create a short cycle: %+v", rows)
	}
}

func TestReconstruct_PerOutcomeIsolation(t *testing.T) {
	// Trades on YES and NO sides of the same market must not be mixed.
	now := time.Now()
	rows := ReconstructRealizedTrades("w-7", []postgres.TradeForReplay{
		tradeAt("BUY", "YES", 0.20, 100, now, "0xa"),
		tradeAt("BUY", "NO", 0.30, 100, now.Add(1*time.Hour), "0xb"),
		tradeAt("SELL", "YES", 0.45, 100, now.Add(2*time.Hour), "0xc"),
	})
	if len(rows) != 1 {
		t.Fatalf("expected 1 cycle on YES, got %d", len(rows))
	}
	if rows[0].Outcome != "YES" {
		t.Fatalf("expected outcome YES, got %s", rows[0].Outcome)
	}
}
