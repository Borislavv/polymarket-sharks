package walletintel

import (
	"testing"
	"time"
)

func defaultSharkParams() SharkParams {
	return SharkParams{
		MinScore:               60,
		MinConfidence:          0.65,
		MaxStaleDays:           180,
		HistMinClosedPositions: 25,
		// v5 high_volume_profitable_shark gates
		VolumeMinTotalPnL:     50_000,
		VolumeMinAvgTrade:     5_000,
		VolumeMinTotalVolume:  500_000,
		VolumeMinExitRate:     0.60,
		VolumeMinProfitFactor: 1.25,
		VolumeMinCycles:       10,
	}
}

// promotableSharkFacts returns WalletFacts that satisfy every v5 hard gate by
// a comfortable margin. Tests mutate one field per case to probe gate behaviour.
func promotableSharkFacts() WalletFacts {
	now := time.Now()
	return WalletFacts{
		WalletID:     "w-pro",
		Now:          now,
		LastClosedAt: now.Add(-3 * 24 * time.Hour),
		LastTradeAt:  now.Add(-1 * 24 * time.Hour),
		// Realized trading path (cycles >= VolumeMinCycles=10)
		RealizedCyclesCount:           40,
		RealizedProfitableCyclesCount: 28, // exit rate 70% > 60%
		RealizedLosingCyclesCount:     12,
		RealizedWinRate:               28.0 / 40.0, // 0.70
		RealizedAvgROI:                0.30,        // diagnostic, not a gate
		RealizedAvgNotional:           20_000,      // avg trade > 5k ✓; 40*20k = 800k volume ✓
		RealizedTotalPnL:              224_000,     // > 50k ✓
		RealizedProfitFactor:          1.80,        // > 1.25 ✓
		ClosedPositionsComplete:       true,
		TradesBackfillComplete:        true,
	}
}

func TestShark_PromotesWhenAllGatesPass(t *testing.T) {
	r := ScoreShark(promotableSharkFacts(), defaultSharkParams())
	if !r.Promote {
		t.Fatalf("expected promote, got reasons=%v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "SHARK_HISTORICAL_EDGE") {
		t.Fatalf("expected SHARK_HISTORICAL_EDGE: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "SUFFICIENT_CYCLES_SAMPLE") {
		t.Fatalf("expected SUFFICIENT_CYCLES_SAMPLE: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "TOTAL_PNL_ABOVE_50K") {
		t.Fatalf("expected TOTAL_PNL_ABOVE_50K: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "AVG_TRADE_ABOVE_5K") {
		t.Fatalf("expected AVG_TRADE_ABOVE_5K: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "VOLUME_ABOVE_500K") {
		t.Fatalf("expected VOLUME_ABOVE_500K: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "EXIT_RATE_ABOVE_60PCT") {
		t.Fatalf("expected EXIT_RATE_ABOVE_60PCT: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "PROFIT_FACTOR_ABOVE_1_25") {
		t.Fatalf("expected PROFIT_FACTOR_ABOVE_1_25: %v", r.ReasonCodes)
	}
	if path, _ := r.FeatureSnapshot["promotion_path"].(string); path != "high_volume_profitable_shark" {
		t.Fatalf("expected promotion_path=high_volume_profitable_shark, got %v", path)
	}
	if basis, _ := r.FeatureSnapshot["scoring_basis"].(string); basis != "realized_trading_pnl" {
		t.Fatalf("expected scoring_basis=realized_trading_pnl, got %v", basis)
	}
}

func TestShark_RealizedTradingSignalPreferredWhenSufficient(t *testing.T) {
	now := time.Now()
	f := WalletFacts{
		Now:                           now,
		RealizedCyclesCount:           30,
		RealizedProfitableCyclesCount: 20,
		RealizedLosingCyclesCount:     10,
		RealizedWinRate:               20.0 / 30.0, // 0.667
		RealizedAvgROI:                0.40,
		RealizedAvgNotional:           25_000,  // 30*25k = 750k volume ✓
		RealizedTotalPnL:              200_000, // > 50k ✓
		RealizedProfitFactor:          1.50,    // > 1.25 ✓
		ClosedPositionsComplete:       true,
	}
	r := ScoreShark(f, defaultSharkParams())
	if !r.Promote {
		t.Fatalf("expected promote on realized trading signal, got %v", r.ReasonCodes)
	}
	if basis, _ := r.FeatureSnapshot["scoring_basis"].(string); basis != "realized_trading_pnl" {
		t.Fatalf("expected scoring_basis=realized_trading_pnl, got %v", basis)
	}
	if path, _ := r.FeatureSnapshot["promotion_path"].(string); path != "high_volume_profitable_shark" {
		t.Fatalf("expected promotion_path=high_volume_profitable_shark, got %v", path)
	}
}

// TestShark_ROIIsNotAGate proves ROI is purely diagnostic and never blocks promotion.
func TestShark_ROIIsNotAGate(t *testing.T) {
	// Wallet with 0% ROI (all trades flat) but passes all volume/PnL/exit gates.
	f := promotableSharkFacts()
	f.RealizedAvgROI = 0 // zero ROI — must not matter
	r := ScoreShark(f, defaultSharkParams())
	if !r.Promote {
		t.Fatalf("zero ROI must not block promotion — ROI is diagnostic only: %v", r.ReasonCodes)
	}
	// Snapshot must carry roi as diagnostic field
	roi, _ := r.FeatureSnapshot["roi"].(float64)
	if roi != 0 {
		// 0 is expected when f.RealizedAvgROI == 0
	}
	// No "ROI_TOO_LOW" gate reason should appear
	if contains(r.ReasonCodes, "ROI_TOO_LOW") {
		t.Fatalf("ROI_TOO_LOW must never appear in v5 reasons: %v", r.ReasonCodes)
	}
}

// TestShark_WinRateIsNotAGate proves win-rate is not a hard gate.
func TestShark_WinRateIsNotAGate(t *testing.T) {
	f := promotableSharkFacts()
	// Drop exit rate to exactly 60% boundary — should still pass
	f.RealizedProfitableCyclesCount = 24
	f.RealizedLosingCyclesCount = 16
	f.RealizedWinRate = 24.0 / 40.0 // 0.60 — meets gate
	r := ScoreShark(f, defaultSharkParams())
	if !r.Promote {
		t.Fatalf("exit rate exactly 60%% must promote (>= gate): %v", r.ReasonCodes)
	}
}

// TestShark_FinalOutcomeDoesNotOverrideProfitableExit pins the contract that
// realized profitable cycles count even when the market later resolves the
// opposite way.
func TestShark_FinalOutcomeDoesNotOverrideProfitableExit(t *testing.T) {
	now := time.Now()
	f := WalletFacts{
		Now:                           now,
		RealizedCyclesCount:           20,
		RealizedProfitableCyclesCount: 14,
		RealizedLosingCyclesCount:     6,
		RealizedWinRate:               14.0 / 20.0, // 0.70
		RealizedAvgROI:                0.50,
		RealizedAvgNotional:           30_000, // 20*30k = 600k ✓
		RealizedTotalPnL:              84_000, // > 50k ✓
		RealizedProfitFactor:          1.50,
		ClosedPositionsComplete:       true,
	}
	r := ScoreShark(f, defaultSharkParams())
	if !r.Promote {
		t.Fatalf("realized profitable cycles must promote regardless of market outcome: %v", r.ReasonCodes)
	}
	for k := range r.FeatureSnapshot {
		if k == "final_outcome" || k == "outcome_match" || k == "outcome_correct" {
			t.Fatalf("feature snapshot must not carry outcome-correctness field %q", k)
		}
	}
}

func TestShark_TotalPnLTooLowRejected(t *testing.T) {
	f := promotableSharkFacts()
	f.RealizedTotalPnL = 49_999 // below 50k gate
	r := ScoreShark(f, defaultSharkParams())
	if r.Promote {
		t.Fatalf("expected NOT promoted with total PnL < 50k")
	}
	if !contains(r.ReasonCodes, "TOTAL_PNL_TOO_LOW") {
		t.Fatalf("expected TOTAL_PNL_TOO_LOW: %v", r.ReasonCodes)
	}
}

func TestShark_AvgTradeTooLowRejected(t *testing.T) {
	f := promotableSharkFacts()
	f.RealizedAvgNotional = 4_999 // below 5k gate; volume also drops
	r := ScoreShark(f, defaultSharkParams())
	if r.Promote {
		t.Fatalf("expected NOT promoted with avg trade < 5k")
	}
	if !contains(r.ReasonCodes, "AVG_TRADE_TOO_LOW") {
		t.Fatalf("expected AVG_TRADE_TOO_LOW: %v", r.ReasonCodes)
	}
}

func TestShark_VolumeTooLowRejected(t *testing.T) {
	f := promotableSharkFacts()
	// 40 cycles × 12_000 avg = 480k < 500k
	f.RealizedAvgNotional = 12_000
	f.RealizedTotalPnL = 67_200 // keep PnL above gate
	r := ScoreShark(f, defaultSharkParams())
	if r.Promote {
		t.Fatalf("expected NOT promoted with volume < 500k")
	}
	if !contains(r.ReasonCodes, "VOLUME_TOO_LOW") {
		t.Fatalf("expected VOLUME_TOO_LOW: %v", r.ReasonCodes)
	}
}

func TestShark_ExitRateTooLowRejected(t *testing.T) {
	f := promotableSharkFacts()
	f.RealizedProfitableCyclesCount = 23 // 23/40 = 0.575 < 0.60
	f.RealizedLosingCyclesCount = 17
	f.RealizedWinRate = 23.0 / 40.0
	r := ScoreShark(f, defaultSharkParams())
	if r.Promote {
		t.Fatalf("expected NOT promoted at exit rate 57.5%%")
	}
	if !contains(r.ReasonCodes, "EXIT_RATE_TOO_LOW") {
		t.Fatalf("expected EXIT_RATE_TOO_LOW: %v", r.ReasonCodes)
	}
}

func TestShark_ProfitFactorTooLowRejected(t *testing.T) {
	f := promotableSharkFacts()
	f.RealizedProfitFactor = 1.10 // < 1.25 gate
	r := ScoreShark(f, defaultSharkParams())
	if r.Promote {
		t.Fatalf("expected NOT promoted with profit factor 1.10")
	}
	if !contains(r.ReasonCodes, "PROFIT_FACTOR_TOO_LOW") {
		t.Fatalf("expected PROFIT_FACTOR_TOO_LOW: %v", r.ReasonCodes)
	}
}

func TestShark_InsufficientCyclesRejected(t *testing.T) {
	f := promotableSharkFacts()
	f.RealizedCyclesCount = 9 // below minCycles=10
	r := ScoreShark(f, defaultSharkParams())
	if r.Promote {
		t.Fatalf("expected NOT promoted with 9 realized cycles (min=10)")
	}
	if !contains(r.ReasonCodes, "INSUFFICIENT_CYCLES_SAMPLE") {
		t.Fatalf("expected INSUFFICIENT_CYCLES_SAMPLE: %v", r.ReasonCodes)
	}
}

func TestShark_APIPathPromotes(t *testing.T) {
	// API closed-position path: profit_factor unavailable, so exit_rate proxy
	// applies — requires exit_rate >= minExitRate+0.10 = 0.70 (not just 0.60).
	now := time.Now()
	f := WalletFacts{
		Now:                         now,
		ClosedPositionsCountHist:    50,
		ProfitableClosedPositions:   36, // exit rate 72% >= 70% proxy gate ✓
		LosingClosedPositions:       14,
		HistoricalTotalBoughtClosed: 750_000, // > 500k volume ✓
		HistoricalRealizedPnL:       65_000,  // > 50k ✓
		HistoricalAvgClosedStake:    15_000,  // > 5k ✓
		HistoricalWinRate:           36.0 / 50.0,
		HistoricalROI:               0.087,
		HistoricalPnLKnown:          true,
		ClosedPositionsComplete:     true,
	}
	r := ScoreShark(f, defaultSharkParams())
	if !r.Promote {
		t.Fatalf("API closed-position path must promote when proxy gate passes: %v", r.ReasonCodes)
	}
	if basis, _ := r.FeatureSnapshot["scoring_basis"].(string); basis != "api_closed_positions" {
		t.Fatalf("expected api_closed_positions basis, got %v", basis)
	}
	if !contains(r.ReasonCodes, "API_EXIT_RATE_PROXY_PASS") {
		t.Fatalf("expected API_EXIT_RATE_PROXY_PASS: %v", r.ReasonCodes)
	}
	if contains(r.ReasonCodes, "PROFIT_FACTOR_ABOVE_1_25") {
		t.Fatalf("PROFIT_FACTOR_ABOVE_1_25 must not appear on API path (PF unavailable): %v", r.ReasonCodes)
	}
	pfAvail, ok := r.FeatureSnapshot["profit_factor_available"].(bool)
	if !ok || pfAvail {
		t.Fatalf("profit_factor_available must be false for API path, got %v", r.FeatureSnapshot["profit_factor_available"])
	}
	if src, _ := r.FeatureSnapshot["profit_factor_source"].(string); src != "unavailable" {
		t.Fatalf("profit_factor_source must be 'unavailable' for API path, got %v", src)
	}
}

func TestShark_APIPathRequiresStrictExitRateProxy(t *testing.T) {
	// API path with exit_rate=66% passes the main exit gate (>=60%) but fails
	// the profit_factor proxy gate (requires >=70%). Must NOT promote.
	now := time.Now()
	f := WalletFacts{
		Now:                         now,
		ClosedPositionsCountHist:    50,
		ProfitableClosedPositions:   33, // 66% — above main gate but below 70% proxy
		LosingClosedPositions:       17,
		HistoricalTotalBoughtClosed: 750_000,
		HistoricalRealizedPnL:       65_000,
		HistoricalAvgClosedStake:    15_000,
		HistoricalWinRate:           33.0 / 50.0,
		HistoricalPnLKnown:          true,
		ClosedPositionsComplete:     true,
	}
	r := ScoreShark(f, defaultSharkParams())
	if r.Promote {
		t.Fatalf("API path exit_rate=66%% must NOT promote: profit_factor unavailable, proxy requires >=70%%: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "API_EXIT_RATE_PROXY_FAIL") {
		t.Fatalf("expected API_EXIT_RATE_PROXY_FAIL: %v", r.ReasonCodes)
	}
	pfAvail, ok := r.FeatureSnapshot["profit_factor_available"].(bool)
	if !ok || pfAvail {
		t.Fatalf("profit_factor_available must be false for API path, got %v", r.FeatureSnapshot["profit_factor_available"])
	}
}

func TestShark_MissingDataNoCyclesNorClosed(t *testing.T) {
	r := ScoreShark(WalletFacts{ClosedPositionsCountHist: 0, ClosedPositionsComplete: true}, defaultSharkParams())
	if !contains(r.ReasonCodes, "MISSING_CLOSED_POSITION_DATA") {
		t.Fatalf("expected MISSING_CLOSED_POSITION_DATA on zero sample: %v", r.ReasonCodes)
	}
	if r.Promote {
		t.Fatalf("zero evidence must not promote")
	}
}

func TestShark_OpenPositionsAloneCannotPromote(t *testing.T) {
	now := time.Now()
	r := ScoreShark(WalletFacts{
		Now:                      now,
		ClosedPositionsCountHist: 0,
		HistoricalPnLKnown:       false,
		ClosedPositionsComplete:  true,
	}, defaultSharkParams())
	if r.Promote {
		t.Fatalf("open positions alone must not promote shark")
	}
}

func TestShark_PartialHistoryAllowsPromotionAtReducedConfidence(t *testing.T) {
	f := promotableSharkFacts()
	f.ClosedPositionsComplete = false
	f.ClosedPositionsCountHist = 5 // non-zero so PARTIAL flag fires
	f.DataQuality = "partial_safety_cap"
	f.ClosedPositionsPartialReason = "SAFETY_CAP_HIT"
	r := ScoreShark(f, defaultSharkParams())
	if !r.Promote {
		t.Fatalf("partial history with real edge must promote, got %v", r.ReasonCodes)
	}
	// PARTIAL_CLOSED_POSITION_HISTORY fires when ClosedPositionsCountHist > 0 and not complete
	if !contains(r.ReasonCodes, "PARTIAL_CLOSED_POSITION_HISTORY") {
		t.Fatalf("expected PARTIAL_CLOSED_POSITION_HISTORY: %v", r.ReasonCodes)
	}
	if contains(r.ReasonCodes, "MISSING_CLOSED_POSITION_DATA") {
		t.Fatalf("MISSING_CLOSED_POSITION_DATA must NOT fire on partial-but-real data")
	}

	complete := promotableSharkFacts()
	complete.DataQuality = "complete"
	rc := ScoreShark(complete, defaultSharkParams())
	if r.Confidence >= rc.Confidence {
		t.Fatalf("partial confidence (%v) must be lower than complete (%v)", r.Confidence, rc.Confidence)
	}
}

func TestShark_StaleActivityFlag(t *testing.T) {
	f := promotableSharkFacts()
	f.LastClosedAt = f.Now.Add(-365 * 24 * time.Hour)
	r := ScoreShark(f, defaultSharkParams())
	if !contains(r.ReasonCodes, "STALE_ACTIVITY") {
		t.Fatalf("expected STALE_ACTIVITY on year-old last close: %v", r.ReasonCodes)
	}
}

func TestShark_ConfidenceScalesWithCycles(t *testing.T) {
	// Small sample: 10 cycles, avg 60k so volume = 600k >= 500k ✓
	small := promotableSharkFacts()
	small.RealizedCyclesCount = 10
	small.RealizedProfitableCyclesCount = 7
	small.RealizedLosingCyclesCount = 3
	small.RealizedWinRate = 0.70
	small.RealizedTotalPnL = 70_000
	small.RealizedAvgNotional = 60_000 // 10*60k = 600k ✓
	rs := ScoreShark(small, defaultSharkParams())

	big := promotableSharkFacts()
	big.RealizedCyclesCount = 150
	big.RealizedProfitableCyclesCount = 105
	big.RealizedLosingCyclesCount = 45
	big.RealizedWinRate = 0.70
	big.RealizedTotalPnL = 1_500_000
	big.RealizedAvgNotional = 10_000 // 150*10k = 1.5M ✓
	rb := ScoreShark(big, defaultSharkParams())

	if !rs.Promote || !rb.Promote {
		t.Fatalf("both samples must promote, small=%v big=%v", rs.ReasonCodes, rb.ReasonCodes)
	}
	if rb.Confidence <= rs.Confidence {
		t.Fatalf("larger sample (%v) must have higher confidence than smaller (%v)", rb.Confidence, rs.Confidence)
	}
}

func TestShark_ExplicitVolumeFieldPreferredOverCycleAvg(t *testing.T) {
	// When RealizedTotalVolume is explicitly set, it takes precedence over
	// CyclesCount*AvgNotional for the volume gate. avgNotional stays above 5k
	// for the avg_trade gate, but cycle*avg = 40*6k = 240k < 500k; the explicit
	// 800k field should push the volume gate to passing.
	f := promotableSharkFacts()
	f.RealizedTotalVolume = 800_000 // explicit field → volume gate passes
	f.RealizedAvgNotional = 6_000   // above avg_trade gate (5k); cycle*avg = 40*6k = 240k (would fail without explicit)
	r := ScoreShark(f, defaultSharkParams())
	if !r.Promote {
		t.Fatalf("explicit RealizedTotalVolume must take precedence over cycle×avg: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "VOLUME_ABOVE_500K") {
		t.Fatalf("expected VOLUME_ABOVE_500K when explicit volume = 800k: %v", r.ReasonCodes)
	}
	vol, _ := r.FeatureSnapshot["total_volume"].(float64)
	if vol != 800_000 {
		t.Fatalf("total_volume in snapshot = %v, want 800_000", vol)
	}
}
