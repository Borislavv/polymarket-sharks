package walletintel

import "fmt"

// AlertTier classifies a trade by notional into tier buckets for the profit gate.
type AlertTier string

const (
	TierTiny   AlertTier = "tiny"
	TierSmall  AlertTier = "small"
	TierMedium AlertTier = "medium"
	TierLarge  AlertTier = "large"
	TierMega   AlertTier = "mega"
)

// ProfitGateParams holds configurable thresholds for the profit-tier alert gate.
// All monetary values are USD. Zero fields fall back to production defaults.
type ProfitGateParams struct {
	Enabled bool

	// Tiny: notional < TinyMaxNotional (default 500)
	TinyMaxNotional float64
	TinyMinOdds     float64
	TinyMinProfit   float64

	// Small: [TinyMax, SmallMax) — default 500–2000
	SmallMaxNotional float64
	SmallMinOdds     float64
	SmallMinProfit   float64

	// Medium: [SmallMax, MediumMax) — default 2000–10000
	MediumMaxNotional float64
	MediumMinOdds     float64
	MediumMinProfit   float64

	// Large: [MediumMax, LargeMax) — default 10000–80000
	LargeMaxNotional float64
	LargeMinOdds     float64
	LargeMinProfit   float64

	// Mega: >= MegaMinNotional — default 80000
	MegaMinNotional float64
	MegaMinOdds     float64
	MegaMinProfit   float64
}

// ProfitGateResult holds the full output of EvalProfitGate.
type ProfitGateResult struct {
	Pass        bool
	Tier        AlertTier
	Reason      string  // empty on pass; "profit_gate_<tier>_failed" on fail
	Odds        float64 // 1 / price
	ProfitIfWin float64 // notional * (odds - 1)
	PayoffIfWin float64 // notional * odds
	MinOdds     float64 // tier threshold
	MinProfit   float64 // tier threshold
}

// EvalProfitGate classifies the trade into a tier and tests it against that
// tier's minimum odds and minimum profit-if-win thresholds.
//
// Validation:
//   - price must be > 0 and <= 1; else Reason = "profit_gate_invalid_price"
//   - notional must be > 0; else Reason = "profit_gate_invalid_notional"
//
// Pass rule: odds >= tier.MinOdds AND profit_if_win >= tier.MinProfit.
func EvalProfitGate(notional, price float64, p ProfitGateParams) ProfitGateResult {
	if price <= 0 || price > 1 {
		return ProfitGateResult{Pass: false, Reason: "profit_gate_invalid_price"}
	}
	if notional <= 0 {
		return ProfitGateResult{Pass: false, Reason: "profit_gate_invalid_notional"}
	}

	odds := 1.0 / price
	payoff := notional * odds
	profit := payoff - notional // = notional * (odds - 1)

	tier := profitGateTier(notional, p)
	minOdds, minProfit := profitGateTierThresholds(tier, p)

	pass := odds >= minOdds && profit >= minProfit
	reason := ""
	if !pass {
		reason = "profit_gate_" + string(tier) + "_failed"
	}

	return ProfitGateResult{
		Pass:        pass,
		Tier:        tier,
		Reason:      reason,
		Odds:        odds,
		ProfitIfWin: profit,
		PayoffIfWin: payoff,
		MinOdds:     minOdds,
		MinProfit:   minProfit,
	}
}

func profitGateTier(notional float64, p ProfitGateParams) AlertTier {
	megaMin := pgDefF(p.MegaMinNotional, 80_000)
	mediumMax := pgDefF(p.MediumMaxNotional, 10_000)
	smallMax := pgDefF(p.SmallMaxNotional, 2_000)
	tinyMax := pgDefF(p.TinyMaxNotional, 500)
	switch {
	case notional >= megaMin:
		return TierMega
	case notional >= mediumMax:
		return TierLarge
	case notional >= smallMax:
		return TierMedium
	case notional >= tinyMax:
		return TierSmall
	default:
		return TierTiny
	}
}

func profitGateTierThresholds(tier AlertTier, p ProfitGateParams) (minOdds, minProfit float64) {
	switch tier {
	case TierTiny:
		return pgDefF(p.TinyMinOdds, 10), pgDefF(p.TinyMinProfit, 4_000)
	case TierSmall:
		return pgDefF(p.SmallMinOdds, 7), pgDefF(p.SmallMinProfit, 7_000)
	case TierMedium:
		return pgDefF(p.MediumMinOdds, 4), pgDefF(p.MediumMinProfit, 15_000)
	case TierLarge:
		return pgDefF(p.LargeMinOdds, 2), pgDefF(p.LargeMinProfit, 25_000)
	case TierMega:
		return pgDefF(p.MegaMinOdds, 1.15), pgDefF(p.MegaMinProfit, 10_000)
	}
	return 999, 1e9 // unreachable
}

// pgDefF returns v if v > 0, else def.
func pgDefF(v, def float64) float64 {
	if v > 0 {
		return v
	}
	return def
}

// FormatProfitGateWhyLine returns a plain-text (unescaped) "Why alert" phrase
// to be embedded in the alert body after escaping by the formatter.
func FormatProfitGateWhyLine(r ProfitGateResult) string {
	return fmt.Sprintf(
		"%s-tier profit gate passed / odds x%.2f >= x%.2f / profit %s >= %s",
		string(r.Tier),
		r.Odds, r.MinOdds,
		fmtUSDCompact(r.ProfitIfWin),
		fmtUSDCompact(r.MinProfit),
	)
}

func fmtUSDCompact(v float64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("$%.1fM", v/1_000_000)
	case v >= 1_000:
		return fmt.Sprintf("$%.0fk", v/1_000)
	default:
		return fmt.Sprintf("$%.0f", v)
	}
}
