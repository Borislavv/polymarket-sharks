package walletintel

import (
	"testing"
)

// defaultVetoCfg returns the default veto config for tests (same as zero-value defaults).
func defaultVetoCfg() SharkVetoConfig { return SharkVetoConfig{} }

// --- CheckProfilePnLVeto ---

func TestVeto_MassiveNegativeProfilePnL_HardReject(t *testing.T) {
	v := CheckProfilePnLVeto(-3_300_000, true, defaultVetoCfg())
	if !v.Fired {
		t.Fatal("expected veto to fire for -$3.3M profile PnL")
	}
	if v.Reason != "MASSIVE_NEGATIVE_ALL_TIME_PNL" {
		t.Fatalf("expected MASSIVE_NEGATIVE_ALL_TIME_PNL, got %q", v.Reason)
	}
}

func TestVeto_ModerateNegativeProfilePnL_NegativeVeto(t *testing.T) {
	// Default threshold is -500k; -600k falls in the NEGATIVE range.
	v := CheckProfilePnLVeto(-600_000, true, defaultVetoCfg())
	if !v.Fired {
		t.Fatal("expected veto to fire for -$600k profile PnL")
	}
	if v.Reason != "NEGATIVE_ALL_TIME_PNL" {
		t.Fatalf("expected NEGATIVE_ALL_TIME_PNL, got %q", v.Reason)
	}
}

func TestVeto_PositiveProfilePnL_NoVeto(t *testing.T) {
	v := CheckProfilePnLVeto(50_000, true, defaultVetoCfg())
	if v.Fired {
		t.Fatalf("expected no veto for positive profile PnL, got %q", v.Reason)
	}
}

func TestVeto_ProfilePnLUnknown_NoVeto(t *testing.T) {
	v := CheckProfilePnLVeto(-3_300_000, false, defaultVetoCfg())
	if v.Fired {
		t.Fatal("expected no veto when profile PnL is unknown")
	}
}

// --- CheckProfileContradictionVeto ---

func TestVeto_LocalPositive_ProfileMassivelyNegative_HardReject(t *testing.T) {
	// local +$94k, profile -$3.3M: the real Radiant-Birdhouse case
	v := CheckProfileContradictionVeto(-3_300_000, true, 94_000, defaultVetoCfg())
	if !v.Fired {
		t.Fatal("expected contradiction veto to fire")
	}
	if v.Reason != "PROFILE_PNL_CONTRADICTS_LOCAL_MASSIVE" {
		t.Fatalf("expected PROFILE_PNL_CONTRADICTS_LOCAL_MASSIVE, got %q", v.Reason)
	}
}

func TestVeto_LocalPositive_ProfileModeratelyNegative_Reject(t *testing.T) {
	// local +$94k, profile -$600k: absDiff ($600k) > localPnL ($94k) → contradiction fires.
	// Note: the standalone profile-PnL threshold is -500k; contradiction veto has own check.
	v := CheckProfileContradictionVeto(-600_000, true, 94_000, defaultVetoCfg())
	if !v.Fired {
		t.Fatal("expected contradiction veto to fire for -$600k profile vs +$94k local")
	}
	if v.Reason != "PROFILE_PNL_CONTRADICTS_LOCAL" {
		t.Fatalf("expected PROFILE_PNL_CONTRADICTS_LOCAL, got %q", v.Reason)
	}
}

func TestVeto_LocalPositive_ProfilePositive_NoVeto(t *testing.T) {
	v := CheckProfileContradictionVeto(200_000, true, 94_000, defaultVetoCfg())
	if v.Fired {
		t.Fatalf("expected no veto when profile is positive, got %q", v.Reason)
	}
}

// --- CheckMissingRiskMetricsVeto ---

func TestVeto_NoRealizedCycles_SmallSample_PartialData_Veto(t *testing.T) {
	// API-only path: no profit factor, no realized cycles, small API sample (25 < 30), partial data
	v := CheckMissingRiskMetricsVeto(false, 0, "partial_offset_cap", 25, defaultVetoCfg())
	if !v.Fired {
		t.Fatal("expected missing risk metrics veto to fire for small API sample")
	}
	if v.Reason != "MISSING_RISK_QUALITY_METRICS_PARTIAL_DATA" {
		t.Fatalf("got %q", v.Reason)
	}
}

func TestVeto_NoRealizedCycles_LargeAPIsample_NoVeto(t *testing.T) {
	// 450 closed positions: sufficient API evidence even without profit factor.
	v := CheckMissingRiskMetricsVeto(false, 0, "partial_offset_cap", 450, defaultVetoCfg())
	if v.Fired {
		t.Fatalf("expected no veto for large API sample (450 >= 30), got %q", v.Reason)
	}
}

func TestVeto_HasRealizedCycles_NoVeto(t *testing.T) {
	v := CheckMissingRiskMetricsVeto(false, 5, "partial_offset_cap", 25, defaultVetoCfg())
	if v.Fired {
		t.Fatalf("expected no veto when realized cycles exist, got %q", v.Reason)
	}
}

func TestVeto_ProfitFactorAvailable_NoVeto(t *testing.T) {
	v := CheckMissingRiskMetricsVeto(true, 0, "partial_offset_cap", 25, defaultVetoCfg())
	if v.Fired {
		t.Fatalf("expected no veto when profit factor is available, got %q", v.Reason)
	}
}

func TestVeto_CompleteDataQuality_NoVeto(t *testing.T) {
	// Even with no realized cycles, complete data quality = no veto.
	v := CheckMissingRiskMetricsVeto(false, 0, "complete", 25, defaultVetoCfg())
	if v.Fired {
		t.Fatalf("expected no veto for complete data quality, got %q", v.Reason)
	}
}

func TestVeto_EmptyDataQuality_NoVeto(t *testing.T) {
	// Empty DataQuality (used in unit tests) must not trigger the veto.
	v := CheckMissingRiskMetricsVeto(false, 0, "", 25, defaultVetoCfg())
	if v.Fired {
		t.Fatalf("expected no veto for empty DataQuality, got %q", v.Reason)
	}
}

// --- CheckSampleRatioVeto ---

func TestVeto_25of1718_Veto(t *testing.T) {
	// 25 realized cycles, 1718 total positions, 1693 open = 98.5% unresolved
	v := CheckSampleRatioVeto(1693, 1718, 25, defaultVetoCfg())
	if !v.Fired {
		t.Fatal("expected sample ratio veto for 25/1718")
	}
	if v.Reason != "REALIZED_SAMPLE_TOO_SMALL_FOR_POSITION_UNIVERSE" {
		t.Fatalf("got %q", v.Reason)
	}
}

func TestVeto_SufficientCycles_BypassesRatioVeto(t *testing.T) {
	// Even with high open ratio, if cycles >= MinCyclesForRatioVeto (30), no veto.
	v := CheckSampleRatioVeto(1693, 1718, 30, defaultVetoCfg())
	if v.Fired {
		t.Fatalf("expected no veto when cycles meets threshold, got %q", v.Reason)
	}
}

func TestVeto_ZeroTotal_NoVeto(t *testing.T) {
	// No position data at all (unit-test wallets with no DB rows).
	v := CheckSampleRatioVeto(0, 0, 5, defaultVetoCfg())
	if v.Fired {
		t.Fatalf("expected no veto when total=0, got %q", v.Reason)
	}
}

func TestVeto_LowOpenRatio_NoVeto(t *testing.T) {
	// 70% open is below the 80% threshold.
	v := CheckSampleRatioVeto(70, 100, 5, defaultVetoCfg())
	if v.Fired {
		t.Fatalf("expected no veto for 70%% open ratio, got %q", v.Reason)
	}
}

// --- ApplySharkVetoes integration ---

func TestVeto_ApplyAll_RadiantBirdhouseCase(t *testing.T) {
	// Simulates the exact Radiant-Birdhouse false positive:
	// local +$94k, profile -$3.3M, partial data, 1693/1718 open, 0 realized cycles
	f := WalletFacts{
		ProfileCashPnL:              -3_300_000,
		ProfileCashPnLKnown:         true,
		DataQuality:                 "partial_offset_cap",
		RealizedCyclesCount:         0,
		HistoricalTotalPositionCount: 1718,
		HistoricalOpenPositionCount:  1693,
	}
	promote, vetoReason, extraReasons := ApplySharkVetoes(f, true, false, 25, 94_000, SharkVetoConfig{})
	if promote {
		t.Fatal("expected promote=false after vetoes")
	}
	if vetoReason == "" {
		t.Fatal("expected a veto reason")
	}
	// At least one of the expected veto reasons must be present
	found := false
	for _, r := range extraReasons {
		if r == "MASSIVE_NEGATIVE_ALL_TIME_PNL" || r == "PROFILE_PNL_CONTRADICTS_LOCAL_MASSIVE" ||
			r == "MISSING_RISK_QUALITY_METRICS_PARTIAL_DATA" || r == "REALIZED_SAMPLE_TOO_SMALL_FOR_POSITION_UNIVERSE" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one concrete veto reason, got %v", extraReasons)
	}
}

func TestVeto_ApplyAll_CleanWallet_NeverVetoed(t *testing.T) {
	// A well-evidenced wallet: positive profile PnL, complete data, many realized cycles.
	f := WalletFacts{
		ProfileCashPnL:              500_000,
		ProfileCashPnLKnown:         true,
		DataQuality:                 "complete",
		RealizedCyclesCount:         50,
		HistoricalTotalPositionCount: 100,
		HistoricalOpenPositionCount:  20,
	}
	promote, vetoReason, extraReasons := ApplySharkVetoes(f, true, true, 50, 500_000, SharkVetoConfig{})
	if !promote {
		t.Fatalf("expected promote=true for clean wallet, vetoReason=%q, extraReasons=%v", vetoReason, extraReasons)
	}
	if vetoReason != "" {
		t.Fatalf("expected no veto reason, got %q", vetoReason)
	}
}

func TestVeto_DemotionAlert_ActiveFalsePositive(t *testing.T) {
	// Verify ScoreShark returns promote=false for the Radiant-Birdhouse case.
	f := promotableSharkFacts() // starts with all gates passing
	// Override with Radiant-Birdhouse-like conditions:
	f.ProfileCashPnL = -3_300_000
	f.ProfileCashPnLKnown = true
	f.DataQuality = "partial_offset_cap"
	f.RealizedCyclesCount = 0 // switch to API path
	f.HistoricalTotalPositionCount = 1718
	f.HistoricalOpenPositionCount = 1693
	// API path: use historical close stats instead
	f.ClosedPositionsCountHist = 25
	f.ProfitableClosedPositions = 18
	f.LosingClosedPositions = 7
	f.HistoricalWinRate = 0.72
	f.HistoricalRealizedPnL = 94_166
	f.HistoricalAvgClosedStake = 69_017
	f.HistoricalTotalBoughtClosed = 1_725_425

	r := ScoreShark(f, defaultSharkParams())
	if r.Promote {
		t.Fatal("expected Radiant-Birdhouse case to be rejected by vetoes")
	}
	vr, _ := r.FeatureSnapshot["veto_reason"].(string)
	if vr == "" {
		t.Fatal("expected veto_reason in feature snapshot")
	}
}

func TestVeto_PartialDataNoProfile_WatchOnly_NotActive(t *testing.T) {
	// Local data partial, profile PnL missing → no profile veto, but
	// missing-risk-metrics + sample-ratio vetoes may still fire.
	f := promotableSharkFacts()
	f.RealizedCyclesCount = 0
	f.DataQuality = "partial_offset_cap"
	f.ProfileCashPnLKnown = false // profile not available
	f.HistoricalTotalPositionCount = 1718
	f.HistoricalOpenPositionCount = 1693
	f.ClosedPositionsCountHist = 25
	f.ProfitableClosedPositions = 18
	f.LosingClosedPositions = 7
	f.HistoricalWinRate = 0.72
	f.HistoricalRealizedPnL = 94_166
	f.HistoricalAvgClosedStake = 69_017
	f.HistoricalTotalBoughtClosed = 1_725_425

	r := ScoreShark(f, defaultSharkParams())
	// Even without profile PnL, missing-risk-metrics AND sample-ratio should veto.
	if r.Promote {
		t.Fatal("expected promote=false: missing risk metrics + sample ratio too small")
	}
}
