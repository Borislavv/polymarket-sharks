package walletintel

import "testing"

func TestArbitration_MatureWalletCannotBeInsider(t *testing.T) {
	insider := ScoreResult{
		Strategy:    "insider_like_score",
		Class:       "insider_like",
		Score:       80,
		Confidence:  0.7,
		Promote:     false,
		ReasonCodes: []string{"MATURE_WALLET_NOT_INSIDER_LIKE"},
	}
	d := ScoreArbitrate(ArbitrateInputs{
		Shark:   ScoreResult{Score: 40, Confidence: 0.4, Promote: false},
		Insider: insider,
	})
	if d.FinalClass == "insider_like" {
		t.Fatalf("expected mature wallet to NOT become insider_like, got %v", d.FinalClass)
	}
}

func TestArbitration_NewWalletCannotBeShark(t *testing.T) {
	shark := ScoreResult{
		Strategy:    "shark_score",
		Class:       "shark",
		Score:       30,
		Confidence:  0.3,
		Promote:     false,
		ReasonCodes: []string{"INSUFFICIENT_SAMPLE"},
	}
	insider := ScoreResult{Score: 75, Confidence: 0.7, Promote: true, Class: "insider_like"}
	d := ScoreArbitrate(ArbitrateInputs{
		Shark:   shark,
		Insider: insider,
	})
	if d.FinalClass != "insider_like" {
		t.Fatalf("expected insider_like for new wallet with promote=true, got %v", d.FinalClass)
	}
}

func TestArbitration_ClusterRaisesSeverityNotClass(t *testing.T) {
	shark := ScoreResult{Score: 80, Confidence: 0.8, Promote: true, Class: "shark"}
	d := ScoreArbitrate(ArbitrateInputs{
		Shark:        shark,
		Insider:      ScoreResult{},
		HasCluster:   true,
		ClusterScore: 85,
	})
	if d.FinalClass != "shark" {
		t.Fatalf("cluster must not rewrite class: got %v", d.FinalClass)
	}
	if d.FinalSeverity != "HIGH" {
		t.Fatalf("expected severity raised to HIGH by cluster, got %v", d.FinalSeverity)
	}
}

func TestArbitration_BlockingRulesRiskPreventsUserAlert(t *testing.T) {
	shark := ScoreResult{Score: 85, Confidence: 0.8, Promote: true, Class: "shark"}
	d := ScoreArbitrate(ArbitrateInputs{
		Shark:   shark,
		Insider: ScoreResult{},
		RulesRisk: RulesRiskResult{
			Level: RulesRiskBlocking,
			Score: 95,
		},
	})
	if d.UserAlertAllowed {
		t.Fatalf("expected user alert blocked under RulesRiskBlocking")
	}
	if !d.AdminAlertAllowed {
		t.Fatalf("expected admin alert still allowed")
	}
}

func TestArbitration_HighRulesRiskRaisesUserAlertThreshold(t *testing.T) {
	shark := ScoreResult{Score: 72, Confidence: 0.7, Promote: true, Class: "shark"}
	d := ScoreArbitrate(ArbitrateInputs{
		Shark:   shark,
		Insider: ScoreResult{},
		RulesRisk: RulesRiskResult{
			Level: RulesRiskHigh,
			Score: 60,
		},
	})
	// 72 < 80 (the user-alert threshold under HIGH) → user alert suppressed
	if d.UserAlertAllowed {
		t.Fatalf("expected user alert suppressed (score<80) under HIGH rules risk")
	}
}
