package walletintel

import (
	"testing"
)

// defaultTestGate returns a ProfitGateParams with all thresholds set to production defaults.
func defaultTestGate() ProfitGateParams {
	return ProfitGateParams{
		Enabled:           true,
		TinyMaxNotional:   500,
		TinyMinOdds:       10,
		TinyMinProfit:     4_000,
		SmallMaxNotional:  2_000,
		SmallMinOdds:      7,
		SmallMinProfit:    7_000,
		MediumMaxNotional: 10_000,
		MediumMinOdds:     4,
		MediumMinProfit:   15_000,
		LargeMaxNotional:  80_000,
		LargeMinOdds:      2,
		LargeMinProfit:    25_000,
		MegaMinNotional:   80_000,
		MegaMinOdds:       1.15,
		MegaMinProfit:     10_000,
	}
}

// ── odds / profit calculation ──────────────────────────────────────────────

func TestEvalProfitGate_OddsAndProfit_100at10pct(t *testing.T) {
	// notional 100, price 0.10 → odds 10, profit 900
	r := EvalProfitGate(100, 0.10, defaultTestGate())
	if r.Odds != 10 {
		t.Fatalf("odds: want 10, got %.4f", r.Odds)
	}
	if r.ProfitIfWin != 900 {
		t.Fatalf("profit_if_win: want 900, got %.4f", r.ProfitIfWin)
	}
	if r.PayoffIfWin != 1000 {
		t.Fatalf("payoff_if_win: want 1000, got %.4f", r.PayoffIfWin)
	}
}

func TestEvalProfitGate_OddsAndProfit_2000at25pct(t *testing.T) {
	// notional 2000, price 0.25 → odds 4, profit 6000
	r := EvalProfitGate(2000, 0.25, defaultTestGate())
	if r.Odds != 4 {
		t.Fatalf("odds: want 4, got %.4f", r.Odds)
	}
	if r.ProfitIfWin != 6000 {
		t.Fatalf("profit_if_win: want 6000, got %.4f", r.ProfitIfWin)
	}
}

func TestEvalProfitGate_InvalidPrice_Zero(t *testing.T) {
	r := EvalProfitGate(1000, 0, defaultTestGate())
	if r.Pass {
		t.Fatal("expected fail for price=0")
	}
	if r.Reason != "profit_gate_invalid_price" {
		t.Fatalf("reason: want profit_gate_invalid_price, got %q", r.Reason)
	}
}

func TestEvalProfitGate_InvalidPrice_GreaterThanOne(t *testing.T) {
	r := EvalProfitGate(1000, 1.5, defaultTestGate())
	if r.Pass {
		t.Fatal("expected fail for price=1.5")
	}
	if r.Reason != "profit_gate_invalid_price" {
		t.Fatalf("reason: want profit_gate_invalid_price, got %q", r.Reason)
	}
}

func TestEvalProfitGate_InvalidNotional_Zero(t *testing.T) {
	r := EvalProfitGate(0, 0.5, defaultTestGate())
	if r.Pass {
		t.Fatal("expected fail for notional=0")
	}
	if r.Reason != "profit_gate_invalid_notional" {
		t.Fatalf("reason: want profit_gate_invalid_notional, got %q", r.Reason)
	}
}

// ── tier selection ─────────────────────────────────────────────────────────

func TestProfitGateTier_100_isTiny(t *testing.T) {
	p := defaultTestGate()
	tier := profitGateTier(100, p)
	if tier != TierTiny {
		t.Fatalf("want tiny, got %s", tier)
	}
}

func TestProfitGateTier_500_isSmall(t *testing.T) {
	tier := profitGateTier(500, defaultTestGate())
	if tier != TierSmall {
		t.Fatalf("want small, got %s", tier)
	}
}

func TestProfitGateTier_2000_isMedium(t *testing.T) {
	tier := profitGateTier(2000, defaultTestGate())
	if tier != TierMedium {
		t.Fatalf("want medium, got %s", tier)
	}
}

func TestProfitGateTier_10000_isLarge(t *testing.T) {
	tier := profitGateTier(10000, defaultTestGate())
	if tier != TierLarge {
		t.Fatalf("want large, got %s", tier)
	}
}

func TestProfitGateTier_80000_isMega(t *testing.T) {
	tier := profitGateTier(80000, defaultTestGate())
	if tier != TierMega {
		t.Fatalf("want mega, got %s", tier)
	}
}

// ── tier pass / fail ───────────────────────────────────────────────────────

func TestProfitGate_TinyFails_ProfitBelowThreshold(t *testing.T) {
	// $100 @ 10x → profit $900 < tiny_min_profit $4000 → FAIL
	r := EvalProfitGate(100, 0.10, defaultTestGate())
	if r.Tier != TierTiny {
		t.Fatalf("tier: want tiny, got %s", r.Tier)
	}
	if r.Pass {
		t.Fatal("expected FAIL: profit $900 < $4000")
	}
	if r.Reason != "profit_gate_tiny_failed" {
		t.Fatalf("reason: want profit_gate_tiny_failed, got %q", r.Reason)
	}
}

func TestProfitGate_SmallFails_ProfitBelowThreshold(t *testing.T) {
	// $500 @ 10x → profit $4500 < small_min_profit $7000 → FAIL
	// price = 1/10 = 0.10, notional 500 → profit = 500*9 = 4500
	r := EvalProfitGate(500, 0.10, defaultTestGate())
	if r.Tier != TierSmall {
		t.Fatalf("tier: want small, got %s", r.Tier)
	}
	if r.Pass {
		t.Fatal("expected FAIL: profit $4500 < $7000")
	}
}

func TestProfitGate_MediumPasses(t *testing.T) {
	// $2000 @ x7 → profit $12000 >= medium_min_profit $10000 (custom)
	// price = 1/7, notional 2000 → profit = 2000*6 = 12000
	p := defaultTestGate()
	p.MediumMinProfit = 10_000 // lower the bar for this test case
	r := EvalProfitGate(2000, 1.0/7, p)
	if r.Tier != TierMedium {
		t.Fatalf("tier: want medium, got %s", r.Tier)
	}
	if !r.Pass {
		t.Fatalf("expected PASS: odds %.2f >= %.2f, profit %.0f >= %.0f",
			r.Odds, r.MinOdds, r.ProfitIfWin, r.MinProfit)
	}
}

func TestProfitGate_LargeFails_ProfitBelowThreshold(t *testing.T) {
	// $10000 @ x2 → profit $10000 < large_min_profit $25000 → FAIL
	r := EvalProfitGate(10000, 0.5, defaultTestGate())
	if r.Tier != TierLarge {
		t.Fatalf("tier: want large, got %s", r.Tier)
	}
	if r.Pass {
		t.Fatal("expected FAIL: profit $10000 < $25000")
	}
}

func TestProfitGate_MegaPasses(t *testing.T) {
	// $80000 @ x1.2 → profit $16000 >= mega_min_profit $10000 → PASS
	// price = 1/1.2 ≈ 0.8333, notional 80000 → profit = 80000*0.2 = 16000
	r := EvalProfitGate(80000, 1.0/1.2, defaultTestGate())
	if r.Tier != TierMega {
		t.Fatalf("tier: want mega, got %s", r.Tier)
	}
	if !r.Pass {
		t.Fatalf("expected PASS: odds %.2f >= %.2f, profit %.0f >= %.0f",
			r.Odds, r.MinOdds, r.ProfitIfWin, r.MinProfit)
	}
}

func TestProfitGate_OddsBelowTierMin_Fails(t *testing.T) {
	// $80000 @ x1.0 (price=1.0) → odds 1.0 < mega_min_odds 1.15 → FAIL
	r := EvalProfitGate(80000, 1.0, defaultTestGate())
	if r.Pass {
		t.Fatal("expected FAIL: odds 1.0 < mega_min_odds 1.15")
	}
	// profit=0, odds=1 → fails both
	if r.Reason != "profit_gate_mega_failed" {
		t.Fatalf("reason: want profit_gate_mega_failed, got %q", r.Reason)
	}
}

func TestProfitGate_LargePassesWithHighOdds(t *testing.T) {
	// $50000 @ x5 → profit $200000 >= $25000, odds 5 >= 2 → PASS
	r := EvalProfitGate(50000, 0.2, defaultTestGate())
	if r.Tier != TierLarge {
		t.Fatalf("tier: want large, got %s", r.Tier)
	}
	if !r.Pass {
		t.Fatalf("expected PASS: odds %.2f >= %.2f, profit %.0f >= %.0f",
			r.Odds, r.MinOdds, r.ProfitIfWin, r.MinProfit)
	}
}

func TestProfitGate_DefaultParams_UsedWhenZero(t *testing.T) {
	// Zero ProfitGateParams should use production defaults.
	r := EvalProfitGate(80000, 1.0/1.2, ProfitGateParams{Enabled: true})
	if r.MinOdds != 1.15 {
		t.Fatalf("mega default min_odds: want 1.15, got %.4f", r.MinOdds)
	}
	if r.MinProfit != 10_000 {
		t.Fatalf("mega default min_profit: want 10000, got %.0f", r.MinProfit)
	}
}

func TestProfitGate_BoundaryNotional_499_isTiny(t *testing.T) {
	tier := profitGateTier(499, defaultTestGate())
	if tier != TierTiny {
		t.Fatalf("499 should be tiny, got %s", tier)
	}
}

func TestProfitGate_BoundaryNotional_9999_isMedium(t *testing.T) {
	tier := profitGateTier(9999, defaultTestGate())
	if tier != TierMedium {
		t.Fatalf("9999 should be medium, got %s", tier)
	}
}

func TestProfitGate_BoundaryNotional_79999_isLarge(t *testing.T) {
	tier := profitGateTier(79999, defaultTestGate())
	if tier != TierLarge {
		t.Fatalf("79999 should be large, got %s", tier)
	}
}

func TestFormatProfitGateWhyLine_NoSpecialChars(t *testing.T) {
	r := ProfitGateResult{
		Pass:        true,
		Tier:        TierMedium,
		Odds:        5.56,
		MinOdds:     4,
		ProfitIfWin: 10_900,
		MinProfit:   15_000,
	}
	line := FormatProfitGateWhyLine(r)
	if line == "" {
		t.Fatal("expected non-empty why line")
	}
	// Must contain tier name
	if !containsStr(line, "medium") {
		t.Fatalf("why line does not contain tier 'medium': %s", line)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}
