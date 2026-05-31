package walletintel

import (
	"strings"
	"testing"
)

func defaultInsiderParams() InsiderParams {
	return InsiderParams{
		MaxLifetimeTrades:     3,
		MaxLifetimeMarkets:    3,
		MinNotionalUSD:        20_000,
		MinScore:              70,
		MinConfidence:         0.60,
		LowProbPriceThr:       0.20,
		MinOdds:               3.0,
		MaxLifetimeForCapture: 10,
		HighImpactCategories:  []string{"politics", "geopolitics", "war", "military", "elections"},
	}
}

func freshInsiderFacts() WalletFacts {
	return WalletFacts{
		WalletID:                "w-new",
		LifetimeTradeCount:      1,
		LifetimeProfitableCount: 0,
		LifetimeLosingCount:     0,
		InsiderStreakClean:      true,
		HistoricalPnLKnown:      false,
		NewBet: &NewBetContext{
			Direction:          DirYesBuy,
			Notional:           25_000,
			Price:              0.20, // odds = 5
			Outcome:            "YES",
			MarketSlug:         "russia-strike-2026",
			MarketCategory:     "war",
			MarketIsHighImpact: true,
		},
	}
}

func TestInsider_FirstBigBet_20k_OddsX3_Promotes(t *testing.T) {
	f := freshInsiderFacts()
	f.NewBet.Notional = 20_000
	f.NewBet.Price = 1.0 / 3.0 // exactly 3x odds → boundary pass
	r := ScoreInsiderLike(f, defaultInsiderParams())
	if !r.Promote {
		t.Fatalf("expected promote on first $20k bet at 3x odds: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "FIRST_LARGE_BET_20K") {
		t.Fatalf("expected FIRST_LARGE_BET_20K: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "HIGH_ODDS_3X") {
		t.Fatalf("expected HIGH_ODDS_3X: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "NOT_LEGAL_INSIDER_CLAIM") {
		t.Fatalf("output must always include NOT_LEGAL_INSIDER_CLAIM marker: %v", r.ReasonCodes)
	}
}

func TestInsider_19_999Rejected(t *testing.T) {
	f := freshInsiderFacts()
	f.NewBet.Notional = 19_999
	r := ScoreInsiderLike(f, defaultInsiderParams())
	if r.Promote {
		t.Fatalf("$19,999 must NOT promote")
	}
	if !contains(r.ReasonCodes, "BELOW_NOTIONAL_THRESHOLD") {
		t.Fatalf("expected BELOW_NOTIONAL_THRESHOLD")
	}
}

func TestInsider_OddsX299Rejected(t *testing.T) {
	f := freshInsiderFacts()
	// 2.99x odds → price > 1/3
	f.NewBet.Price = 1.0 / 2.99
	r := ScoreInsiderLike(f, defaultInsiderParams())
	if r.Promote {
		t.Fatalf("odds < 3 must NOT promote")
	}
}

func TestInsider_11LifetimeTradesNotInitialInsiderLike(t *testing.T) {
	f := freshInsiderFacts()
	f.LifetimeTradeCount = 11
	r := ScoreInsiderLike(f, defaultInsiderParams())
	if r.Promote {
		t.Fatalf("11 lifetime trades must NOT be initial insider_like")
	}
	if !contains(r.ReasonCodes, "MATURE_WALLET_NOT_INSIDER_LIKE") {
		t.Fatalf("expected MATURE_WALLET_NOT_INSIDER_LIKE: %v", r.ReasonCodes)
	}
}

func TestInsider_StreakContinues(t *testing.T) {
	f := freshInsiderFacts()
	f.LifetimeTradeCount = 3
	f.LifetimeProfitableCount = 2
	f.LifetimeLosingCount = 0
	f.InsiderStreakClean = true
	f.HistoricalPnLKnown = true
	r := ScoreInsiderLike(f, defaultInsiderParams())
	if !r.Promote {
		t.Fatalf("clean streak with $25k high-odds new bet must promote: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "WINNING_STREAK") {
		t.Fatalf("expected WINNING_STREAK: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "NO_LOSSES_YET") {
		t.Fatalf("expected NO_LOSSES_YET: %v", r.ReasonCodes)
	}
	cont, _ := r.FeatureSnapshot["streak_continues"].(bool)
	if !cont {
		t.Fatalf("expected streak_continues=true")
	}
}

func TestInsider_AnyLossDemotesStreak(t *testing.T) {
	f := freshInsiderFacts()
	f.LifetimeTradeCount = 5
	f.LifetimeProfitableCount = 3
	f.LifetimeLosingCount = 1
	f.InsiderStreakClean = false
	f.HistoricalPnLKnown = true
	r := ScoreInsiderLike(f, defaultInsiderParams())
	if r.Promote {
		t.Fatalf("expected NOT promoted after a loss appears")
	}
	if !contains(r.ReasonCodes, "STREAK_BROKEN") {
		t.Fatalf("expected STREAK_BROKEN: %v", r.ReasonCodes)
	}
}

func TestInsider_MissingDirectionSuppresses(t *testing.T) {
	f := freshInsiderFacts()
	f.NewBet.Direction = ""
	r := ScoreInsiderLike(f, defaultInsiderParams())
	if r.Promote {
		t.Fatalf("missing direction must block promote")
	}
	if !contains(r.MissingData, "MISSING_TRADE_DIRECTION") {
		t.Fatalf("expected MISSING_TRADE_DIRECTION: %v", r.MissingData)
	}
}

func TestInsider_NoNewBetContext_NeutralWithExplicitReasons(t *testing.T) {
	f := WalletFacts{WalletID: "w-noctx", LifetimeTradeCount: 0}
	r := ScoreInsiderLike(f, defaultInsiderParams())
	if r.Promote {
		t.Fatalf("must not promote without NewBet")
	}
	if r.Score != 0 || r.Confidence != 0 {
		t.Fatalf("expected zero score+confidence, got %d / %v", r.Score, r.Confidence)
	}
	if len(r.ReasonCodes) == 0 {
		t.Fatalf("reason_codes must NEVER be nil/empty: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "NO_BIG_BET_CONTEXT") {
		t.Fatalf("expected NO_BIG_BET_CONTEXT: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "ROI_NOT_REQUIRED") {
		t.Fatalf("expected ROI_NOT_REQUIRED: %v", r.ReasonCodes)
	}
	// must NOT carry shark gate codes
	for _, banned := range []string{"ROI_TOO_LOW", "WIN_RATE_TOO_LOW", "AVG_STAKE_TOO_LOW", "INSUFFICIENT_CLOSED_POSITIONS"} {
		if contains(r.ReasonCodes, banned) {
			t.Fatalf("insider must not carry shark reason %q", banned)
		}
	}
}

func TestInsider_BetContext_AlwaysCarriesROINotRequired(t *testing.T) {
	r := ScoreInsiderLike(freshInsiderFacts(), defaultInsiderParams())
	if !contains(r.ReasonCodes, "ROI_NOT_REQUIRED") {
		t.Fatalf("ROI_NOT_REQUIRED must always be present in insider reasons: %v", r.ReasonCodes)
	}
}

func TestArbitration_KeepsStrategiesInOwnNamespace(t *testing.T) {
	// Shark fails multiple v5 gates (no PnL, no volume); insider is neutral (no NewBet).
	// The arbitration result must carry shark's rejection reasons verbatim.
	sharkResult := ScoreShark(WalletFacts{ClosedPositionsCountHist: 10, ClosedPositionsComplete: true}, SharkParams{})
	insiderResult := ScoreInsiderLike(WalletFacts{}, defaultInsiderParams())
	dec := ScoreArbitrate(ArbitrateInputs{Shark: sharkResult, Insider: insiderResult})
	if dec.FinalClass != "rejected" {
		t.Fatalf("expected rejected, got %s", dec.FinalClass)
	}
	// v5 shark reasons appear under their natural names (PnL/volume gates).
	if !contains(dec.ReasonCodes, "TOTAL_PNL_TOO_LOW") {
		t.Fatalf("expected TOTAL_PNL_TOO_LOW from shark namespace: %v", dec.ReasonCodes)
	}
	// Insider's NO_BIG_BET_CONTEXT must be preserved verbatim (already insider-namespace).
	if !contains(dec.ReasonCodes, "NO_BIG_BET_CONTEXT") {
		t.Fatalf("expected NO_BIG_BET_CONTEXT to be present: %v", dec.ReasonCodes)
	}
	// Snapshot preserves both raw reason lists for downstream analytics.
	if _, ok := dec.FeatureSnapshot["shark_reason_codes"]; !ok {
		t.Fatalf("expected shark_reason_codes in snapshot")
	}
	if _, ok := dec.FeatureSnapshot["insider_reason_codes"]; !ok {
		t.Fatalf("expected insider_reason_codes in snapshot")
	}
}

func TestInsider_NoLegalInsiderLanguage(t *testing.T) {
	r := ScoreInsiderLike(freshInsiderFacts(), defaultInsiderParams())
	for _, code := range r.ReasonCodes {
		low := strings.ToLower(code)
		if strings.Contains(low, "confirmed_insider") ||
			strings.Contains(low, "illegal_insider") ||
			strings.Contains(low, "guaranteed") ||
			strings.Contains(low, "knows_outcome") {
			t.Fatalf("forbidden language in reason code: %q", code)
		}
	}
}

func TestInsider_IndependentFromSharkGates(t *testing.T) {
	// An insider-like wallet may have zero closed positions and zero ROI/WR;
	// it must still promote on the first big high-odds bet alone.
	f := freshInsiderFacts()
	f.ClosedPositionsCountHist = 0
	f.HistoricalROI = 0
	f.HistoricalWinRate = 0
	f.HistoricalAvgClosedStake = 0
	r := ScoreInsiderLike(f, defaultInsiderParams())
	if !r.Promote {
		t.Fatalf("insider-like must NOT be blocked by shark ROI/win-rate gates: %v", r.ReasonCodes)
	}
}

func TestInsider_LowImpactCategoryRejected(t *testing.T) {
	f := freshInsiderFacts()
	f.NewBet.MarketIsHighImpact = false
	f.NewBet.MarketCategory = "sports"
	r := ScoreInsiderLike(f, defaultInsiderParams())
	if r.Promote {
		t.Fatalf("low-impact market must not promote initial insider-like")
	}
}
