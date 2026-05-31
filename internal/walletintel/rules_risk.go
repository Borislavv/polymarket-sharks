package walletintel

import "strings"

// RulesRiskLevel classifies how ambiguous a market's resolution is.
// Confidence and alert routing depend on the level.
type RulesRiskLevel string

const (
	RulesRiskLow      RulesRiskLevel = "LOW"
	RulesRiskMedium   RulesRiskLevel = "MEDIUM"
	RulesRiskHigh     RulesRiskLevel = "HIGH"
	RulesRiskBlocking RulesRiskLevel = "BLOCKING"
)

// MarketRulesInputs is what the rules_risk_modifier sees.
type MarketRulesInputs struct {
	Title               string
	Question            string
	Description         string
	ResolutionSource    string
	RulesText           string
	EventTitle          string
	NegRiskFlagged      bool
	UMAUncertainty      bool
	UMAResolutionStatus string // "proposed"/"resolved"/"disputed"/"challenged"
	Disputed            bool
	HasOfficialSource   bool
}

// RulesRiskResult — output of rules_risk_modifier.
type RulesRiskResult struct {
	Score       int // 0..100 (higher = riskier)
	Level       RulesRiskLevel
	ReasonCodes []string
	Explanation string
}

var vaguePhrases = []string{
	"any time", "anytime", "anywhere", "anybody", "anyone",
	"will be considered", "may be considered",
	"is generally", "typically", "broadly",
	"sufficiently", "materially",
	"in the news",
}

var subjectiveWords = []string{
	"believe", "think", "appear", "seem", "deem",
	"sufficient", "reasonable", "significant", "credible",
}

// EvaluateRulesRisk inspects market metadata and emits a deterministic
// risk classification. Pure function; no IO.
func EvaluateRulesRisk(m MarketRulesInputs) RulesRiskResult {
	var reasons []string
	score := 0
	parts := []string{}

	blob := strings.ToLower(strings.Join([]string{
		m.Title, m.Question, m.Description, m.RulesText, m.ResolutionSource,
	}, " | "))

	if strings.TrimSpace(m.ResolutionSource) == "" && !m.HasOfficialSource {
		score += 25
		reasons = append(reasons, "NO_RESOLUTION_SOURCE")
		parts = append(parts, "resolution source missing")
	}
	for _, w := range vaguePhrases {
		if strings.Contains(blob, w) {
			score += 8
			reasons = append(reasons, "VAGUE_PHRASE")
			parts = append(parts, "vague phrase '"+w+"'")
			break
		}
	}
	for _, w := range subjectiveWords {
		if strings.Contains(blob, w) {
			score += 6
			reasons = append(reasons, "SUBJECTIVE_WORDING")
			parts = append(parts, "subjective wording")
			break
		}
	}
	if strings.Contains(blob, "tweet") || strings.Contains(blob, "post") || strings.Contains(blob, "social media") {
		score += 10
		reasons = append(reasons, "SOCIAL_MEDIA_AS_SOURCE")
		parts = append(parts, "source-of-truth is social media")
	}
	if strings.Contains(blob, "will x happen") ||
		strings.Contains(blob, "ever happen") ||
		strings.Contains(blob, "by end of") {
		score += 12
		reasons = append(reasons, "AMBIGUOUS_DEADLINE")
		parts = append(parts, "deadline ambiguous")
	}
	if m.UMAUncertainty {
		score += 20
		reasons = append(reasons, "UMA_UNCERTAINTY")
		parts = append(parts, "UMA uncertainty flagged")
	}
	switch strings.ToLower(m.UMAResolutionStatus) {
	case "disputed", "challenged":
		score += 30
		reasons = append(reasons, "UMA_DISPUTED")
		parts = append(parts, "UMA "+m.UMAResolutionStatus)
	case "proposed":
		score += 10
		reasons = append(reasons, "UMA_PROPOSED")
		parts = append(parts, "UMA proposed (pending)")
	}
	if m.NegRiskFlagged {
		score += 10
		reasons = append(reasons, "NEG_RISK_FLAGGED")
		parts = append(parts, "negRisk flagged")
	}
	if m.Disputed {
		score += 30
		reasons = append(reasons, "RESOLUTION_DISPUTED")
		parts = append(parts, "resolution disputed")
	}

	if score > 100 {
		score = 100
	}

	level := RulesRiskLow
	switch {
	case score >= 75 || m.Disputed:
		level = RulesRiskBlocking
		reasons = append(reasons, "RULES_RISK_BLOCKING")
	case score >= 45:
		level = RulesRiskHigh
		reasons = append(reasons, "RULES_RISK_HIGH")
	case score >= 20:
		level = RulesRiskMedium
		reasons = append(reasons, "RULES_RISK_MEDIUM")
	}

	explanation := strings.Join(parts, "; ")
	if explanation == "" {
		explanation = "no ambiguity markers detected"
	}

	return RulesRiskResult{
		Score:       score,
		Level:       level,
		ReasonCodes: reasons,
		Explanation: explanation,
	}
}

// ApplyRulesRiskToConfidence returns confidence after rules-risk discount.
func ApplyRulesRiskToConfidence(conf float64, level RulesRiskLevel) float64 {
	switch level {
	case RulesRiskMedium:
		return conf * 0.85
	case RulesRiskHigh:
		return conf * 0.6
	case RulesRiskBlocking:
		return conf * 0.4
	}
	return conf
}
