package walletintel

// FinalDecision is the output of score_arbitration: a single canonical
// verdict for downstream watchlist/alert routing.
type FinalDecision struct {
	FinalClass        string // "shark" | "insider_like" | "rejected" | "watch_only" | "admin_only"
	FinalScore        int
	FinalConfidence   float64
	FinalSeverity     string // "INFO" | "WARNING" | "HIGH"
	ReasonCodes       []string
	MissingData       []string
	Promote           bool
	UserAlertAllowed  bool
	AdminAlertAllowed bool
	FeatureSnapshot   FeatureSnapshot
}

// ArbitrateInputs bundles all evidence inspected by score_arbitration.
type ArbitrateInputs struct {
	Shark        ScoreResult
	Insider      ScoreResult
	RulesRisk    RulesRiskResult
	HasCluster   bool
	ClusterScore int
}

// ScoreArbitrate reconciles shark vs insider vs rules risk vs cluster.
//
// Rules implemented:
//   - Mature wallet cannot become insider_like (insider strategy already
//     rejects, this enforces defensively at arbitration).
//   - New wallet cannot become shark (shark requires sample-size pass).
//   - Negative-PnL mature wallet not promoted purely on large exposure.
//   - Cluster evidence can raise severity but never rewrites class.
//   - Rules risk: HIGH raises score threshold and lowers confidence;
//     BLOCKING forbids user-facing alerts but keeps admin alert allowed.
//   - Missing data lowers confidence and records reason codes.
func ScoreArbitrate(in ArbitrateInputs) FinalDecision {
	var reasons []string
	var missing []string
	// Track which namespace contributed which reason so the final result
	// preserves strategy-attribution. Without this, an insider rejection
	// could be polluted with shark ROI_TOO_LOW reasons and vice versa.
	missing = mergeUnique(missing, in.Shark.MissingData)
	missing = mergeUnique(missing, in.Insider.MissingData)

	// pick the dominant strategy
	var chosen ScoreResult
	chosenIs := "" // "shark" | "insider_like" | ""
	if in.Shark.Promote && in.Insider.Promote {
		// favor shark when sample is sufficient (mature)
		if !contains(in.Shark.ReasonCodes, "INSUFFICIENT_SAMPLE") {
			chosen = in.Shark
			chosenIs = "shark"
		} else {
			chosen = in.Insider
			chosenIs = "insider_like"
		}
	} else if in.Shark.Promote {
		chosen = in.Shark
		chosenIs = "shark"
	} else if in.Insider.Promote {
		chosen = in.Insider
		chosenIs = "insider_like"
	}

	// Compose final reason_codes per strategy namespace.
	//   - promoted shark    → shark reasons + rules risk
	//   - promoted insider  → insider reasons + rules risk
	//   - both fail/neutral → both namespaces but ATTRIBUTED so consumers
	//     can tell which strategy emitted which code.
	switch chosenIs {
	case "shark":
		reasons = mergeUnique(reasons, in.Shark.ReasonCodes)
	case "insider_like":
		reasons = mergeUnique(reasons, in.Insider.ReasonCodes)
	default:
		// Both failed/neutral. Keep shark reasons under their natural names,
		// prefix insider reasons with INSIDER_ so they cannot be mistaken
		// for shark gates. The chosen-strategy snapshot below preserves the
		// raw insider reasons for downstream analytics.
		reasons = mergeUnique(reasons, in.Shark.ReasonCodes)
		for _, r := range in.Insider.ReasonCodes {
			// Insider reasons that are already self-descriptive (LIKE_,
			// INSIDER_*, FIRST_*, STREAK_*) are not re-prefixed.
			if isInsiderNamespace(r) {
				reasons = appendUnique(reasons, r)
			} else {
				reasons = appendUnique(reasons, "INSIDER_"+r)
			}
		}
	}
	reasons = mergeUnique(reasons, in.RulesRisk.ReasonCodes)

	finalClass := "rejected"
	finalScore := 0
	finalConf := 0.0
	severity := "INFO"

	if chosenIs == "shark" {
		finalClass = "shark"
		finalScore = chosen.Score
		finalConf = chosen.Confidence
		severity = "WARNING"
	} else if chosenIs == "insider_like" {
		finalClass = "insider_like"
		finalScore = chosen.Score
		finalConf = chosen.Confidence
		severity = "HIGH"
	} else {
		// pick the higher-scored result for diagnostics, but no promote
		if in.Shark.Score >= in.Insider.Score {
			finalScore = in.Shark.Score
			finalConf = in.Shark.Confidence
		} else {
			finalScore = in.Insider.Score
			finalConf = in.Insider.Confidence
		}
	}

	// Cluster raises severity only
	if in.HasCluster && in.ClusterScore >= 70 {
		if severity == "WARNING" {
			severity = "HIGH"
		}
		reasons = appendUnique(reasons, "CLUSTER_EVIDENCE")
	}

	// rules risk modulation
	finalConf = ApplyRulesRiskToConfidence(finalConf, in.RulesRisk.Level)
	userAllowed := finalClass != "rejected"
	adminAllowed := true
	if in.RulesRisk.Level == RulesRiskBlocking {
		userAllowed = false
		finalClass = pickIf(finalClass == "rejected", "rejected", "admin_only")
		severity = "INFO"
	} else if in.RulesRisk.Level == RulesRiskHigh {
		// require higher score for user alert
		minForUserAlert := 80
		if finalScore < minForUserAlert {
			userAllowed = false
			finalClass = pickIf(finalClass == "rejected", "rejected", "watch_only")
		}
	}

	promote := finalClass == "shark" || finalClass == "insider_like"
	if !promote && finalClass == "watch_only" {
		// watch_only means insufficient for user-facing promo but worth tracking
		promote = false
	}

	// Anti-conflict guard: ensure we never label a mature wallet as insider_like
	// (insider strategy already rejects mature, this is a defensive check).
	if finalClass == "insider_like" && contains(in.Insider.ReasonCodes, "MATURE_WALLET_NOT_INSIDER_LIKE") {
		finalClass = "rejected"
		promote = false
		userAllowed = false
	}

	snap := FeatureSnapshot{
		"shark_score":          in.Shark.Score,
		"shark_confidence":     in.Shark.Confidence,
		"shark_promote":        in.Shark.Promote,
		"shark_reason_codes":   in.Shark.ReasonCodes,
		"insider_score":        in.Insider.Score,
		"insider_confidence":   in.Insider.Confidence,
		"insider_promote":      in.Insider.Promote,
		"insider_reason_codes": in.Insider.ReasonCodes,
		"rules_risk_score":     in.RulesRisk.Score,
		"rules_risk_level":     string(in.RulesRisk.Level),
		"has_cluster":          in.HasCluster,
		"cluster_score":        in.ClusterScore,
		"final_class":          finalClass,
		"final_score":          finalScore,
		"final_confidence":     finalConf,
		"final_severity":       severity,
		"user_alert_allowed":   userAllowed,
		"admin_alert_allowed":  adminAllowed,
	}

	return FinalDecision{
		FinalClass:        finalClass,
		FinalScore:        finalScore,
		FinalConfidence:   finalConf,
		FinalSeverity:     severity,
		ReasonCodes:       reasons,
		MissingData:       missing,
		Promote:           promote,
		UserAlertAllowed:  userAllowed,
		AdminAlertAllowed: adminAllowed,
		FeatureSnapshot:   snap,
	}
}

func mergeUnique(a, b []string) []string {
	for _, x := range b {
		a = appendUnique(a, x)
	}
	return a
}

func pickIf(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// isInsiderNamespace reports whether a reason code is self-evidently insider-
// specific. Such codes are merged into the arbitrated reason list verbatim;
// shark-namespace codes (ROI_*, WIN_RATE_*, AVG_STAKE_*, CLOSED_POSITION_*,
// PNL-only) coming from a failed insider strategy get prefixed with INSIDER_
// to prevent semantic collisions in admin/audit views.
func isInsiderNamespace(r string) bool {
	switch r {
	case "NO_BIG_BET_CONTEXT",
		"NOT_LEGAL_INSIDER_CLAIM",
		"ROI_NOT_REQUIRED",
		"MISSING_TRADE_DIRECTION",
		"MISSING_RESOLUTION_DATA",
		"MATURE_WALLET_NOT_INSIDER_LIKE",
		"INSIDER_LIKE_CANDIDATE",
		"NEW_WALLET",
		"LOW_LIFETIME_HISTORY",
		"FIRST_LARGE_BET_20K",
		"HIGH_ODDS_3X",
		"HIGH_IMPACT_MARKET",
		"NO_LOSSES_YET",
		"WINNING_STREAK",
		"STREAK_BROKEN",
		"LARGE_ANONYMOUS_CONVICTION",
		"BELOW_NOTIONAL_THRESHOLD",
		"ODDS_BELOW_3X",
		"FIRST_WIN_CONFIRMED",
		"STREAK_CONTINUES":
		return true
	}
	return len(r) >= 8 && r[:8] == "INSIDER_"
}
