package walletintel

import "testing"

func TestRulesRisk_ClearMarketLow(t *testing.T) {
	r := EvaluateRulesRisk(MarketRulesInputs{
		Title:             "Will Candidate X win the 2026 US presidential election?",
		Description:       "Resolves YES if Candidate X is sworn in as President.",
		ResolutionSource:  "ap.org",
		HasOfficialSource: true,
	})
	if r.Level != RulesRiskLow {
		t.Fatalf("expected LOW level got %v (reasons=%v)", r.Level, r.ReasonCodes)
	}
}

func TestRulesRisk_AmbiguousResolutionHigh(t *testing.T) {
	r := EvaluateRulesRisk(MarketRulesInputs{
		Title:       "Will X ever happen anytime by end of next quarter?",
		Description: "Resolves based on tweets and social media posts considered sufficient by the team.",
	})
	if r.Level != RulesRiskHigh && r.Level != RulesRiskBlocking {
		t.Fatalf("expected HIGH/BLOCKING, got %v (score=%d reasons=%v)", r.Level, r.Score, r.ReasonCodes)
	}
}

func TestRulesRisk_HighReducesConfidence(t *testing.T) {
	high := RulesRiskResult{Level: RulesRiskHigh}
	conf := ApplyRulesRiskToConfidence(1.0, high.Level)
	if conf >= 1.0 {
		t.Fatalf("expected confidence reduced under HIGH, got %v", conf)
	}
}

func TestRulesRisk_BlockingDoesNotSilenceAdmin(t *testing.T) {
	r := EvaluateRulesRisk(MarketRulesInputs{
		Title:    "disputed market",
		Disputed: true,
	})
	if r.Level != RulesRiskBlocking {
		t.Fatalf("expected BLOCKING, got %v", r.Level)
	}
	// Admin alert allowed is enforced in arbitration; here we just verify
	// that the result is marked BLOCKING and confidence is heavily reduced.
	conf := ApplyRulesRiskToConfidence(1.0, r.Level)
	if conf > 0.5 {
		t.Fatalf("expected sharply reduced confidence on BLOCKING, got %v", conf)
	}
}
