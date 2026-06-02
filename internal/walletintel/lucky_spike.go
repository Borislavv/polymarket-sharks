package walletintel

import (
	"math"
	"strings"
	"time"
)

// LuckySpikeFacts is the weekly evidence snapshot used by lucky_spike_score.
// It focuses on sustained high-frequency execution and realized profitability.
type LuckySpikeFacts struct {
	WeeklyTradeCount        int
	WeeklyDistinctMarkets   int
	WeeklyRealizedCycles    int
	WeeklyProfitableCycles  int
	WeeklyLosingCycles      int
	WeeklyCoverage          time.Duration
	WeeklyAvgTradeInterval  time.Duration
	WeeklyAvgTradeNotional  float64
	WeeklyRealizedPnL       float64
	WeeklyEntryNotional     float64
	WeeklyProfitPct         float64
	WeeklyProfitPctKnown    bool
	WeeklyProfitSource      string
	WeeklyProfitPositions   int
	MonthlyTradeCount       int
	MonthlyRealizedCycles   int
	MonthlyCoverage         time.Duration
	MonthlyAvgTradeInterval time.Duration
	MonthlyRealizedPnL      float64
	MonthlyEntryNotional    float64
	MonthlyProfitPct        float64
	MonthlyProfitPctKnown   bool
	MonthlyProfitSource     string
	MonthlyProfitPositions  int
	LastTradeAt             time.Time
	DataQuality             string // complete | partial_* | proxy | missing
	TradeHistoryPartialHint string // DATA_API_OFFSET_CAP_3000 | LOCAL_PAGE_CAP | ""
}

// LuckySpikeParams controls lucky_spike_score thresholds.
type LuckySpikeParams struct {
	Lookback            time.Duration // default: 7d
	MaxAvgTradeInterval time.Duration // default: 2m
	MinProfitPct        float64       // default: 0.30
	MinTradesPerWeek    int           // default: lookback / max interval (5040 for 7d/2m)
	MinTradesPerMonth   int           // default: 30d / max interval (21600 for 30d/2m)
	MinCoverage         time.Duration // default: 6d (sustained, not a one-day burst)
	MinObservedTrades   int           // default: 1000, cap-aware lower-bound sample
	MinObservedCoverage time.Duration // default: 48h, cap-aware lower-bound span
	MinEntryNotional    float64       // optional absolute stake floor, default: 0
	MinRealizedPnL      float64       // optional absolute pnl floor, default: 0
	MinRealizedCycles   int           // default: 30
	MinScore            int           // default: 75
	MinConfidence       float64       // default: 0.70
}

// ScoreLuckySpike flags wallets with a suspicious weekly "luck spike":
// sustained high-frequency trading (>=1 trade / 2m on average)
// plus elevated realized profitability (realized_pnl / entry_notional > 30%).
//
// Output class is insider_like (suspicious informed-flow candidate), but this
// strategy is intended for admin-led review rather than direct legal claims.
func ScoreLuckySpike(f LuckySpikeFacts, p LuckySpikeParams) ScoreResult {
	lookback := p.Lookback
	if lookback <= 0 {
		lookback = 7 * 24 * time.Hour
	}
	maxInterval := p.MaxAvgTradeInterval
	if maxInterval <= 0 {
		maxInterval = 2 * time.Minute
	}
	minProfitPct := p.MinProfitPct
	if minProfitPct <= 0 {
		minProfitPct = 0.30
	}
	minTrades := p.MinTradesPerWeek
	if minTrades <= 0 {
		minTrades = int(lookback / maxInterval)
	}
	minTradesMonth := p.MinTradesPerMonth
	if minTradesMonth <= 0 {
		minTradesMonth = int((30 * 24 * time.Hour) / maxInterval)
	}
	minCoverage := p.MinCoverage
	if minCoverage <= 0 {
		minCoverage = 6 * 24 * time.Hour
	}
	minObservedCoverage := p.MinObservedCoverage
	if minObservedCoverage <= 0 {
		minObservedCoverage = 48 * time.Hour
	}
	minObservedTrades := p.MinObservedTrades
	if minObservedTrades <= 0 {
		minObservedTrades = 1000
	}
	minEntryNotional := p.MinEntryNotional
	if minEntryNotional < 0 {
		minEntryNotional = 0
	}
	minRealizedPnL := p.MinRealizedPnL
	if minRealizedPnL < 0 {
		minRealizedPnL = 0
	}
	minCycles := p.MinRealizedCycles
	if minCycles <= 0 {
		minCycles = 30
	}
	minScore := p.MinScore
	if minScore <= 0 {
		minScore = 75
	}
	minConf := p.MinConfidence
	if minConf <= 0 {
		minConf = 0.70
	}

	var (
		reasons []string
		missing []string
	)

	weeklyAvgInterval := f.WeeklyAvgTradeInterval
	if weeklyAvgInterval <= 0 && f.WeeklyTradeCount > 0 {
		weeklyAvgInterval = time.Duration(float64(lookback) / float64(f.WeeklyTradeCount))
	}
	monthlyAvgInterval := f.MonthlyAvgTradeInterval
	if monthlyAvgInterval <= 0 && f.MonthlyTradeCount > 0 {
		monthlyAvgInterval = time.Duration(float64(30*24*time.Hour) / float64(f.MonthlyTradeCount))
	}

	// Hard gates.
	weeklyIntervalOK := weeklyAvgInterval > 0 && weeklyAvgInterval <= maxInterval
	monthlyIntervalOK := monthlyAvgInterval > 0 && monthlyAvgInterval <= maxInterval
	weeklyStrictFrequencyOK := f.WeeklyTradeCount >= minTrades && weeklyIntervalOK && f.WeeklyCoverage >= minCoverage
	monthlyStrictFrequencyOK := f.MonthlyTradeCount >= minTradesMonth && monthlyIntervalOK && f.MonthlyCoverage >= minCoverage
	weeklyObservedFrequencyOK := f.WeeklyTradeCount >= minObservedTrades && weeklyIntervalOK && f.WeeklyCoverage >= minObservedCoverage
	monthlyObservedFrequencyOK := f.MonthlyTradeCount >= minObservedTrades && monthlyIntervalOK && f.MonthlyCoverage >= minObservedCoverage
	weeklyFrequencyOK := weeklyStrictFrequencyOK || weeklyObservedFrequencyOK
	monthlyFrequencyOK := monthlyStrictFrequencyOK || monthlyObservedFrequencyOK
	frequencyOK := weeklyFrequencyOK || monthlyFrequencyOK
	strictCoverageOK := f.WeeklyCoverage >= minCoverage || f.MonthlyCoverage >= minCoverage
	observedCoverageOK := f.WeeklyCoverage >= minObservedCoverage || f.MonthlyCoverage >= minObservedCoverage
	coverageOK := strictCoverageOK || observedCoverageOK
	positionSample := maxInt(f.WeeklyProfitPositions, f.MonthlyProfitPositions)
	reconstructedSample := maxInt(f.WeeklyRealizedCycles, f.MonthlyRealizedCycles)
	sampleCount := maxInt(positionSample, reconstructedSample)
	cyclesOK := sampleCount >= minCycles
	weeklyProfitAmountOK := f.WeeklyEntryNotional >= minEntryNotional && f.WeeklyRealizedPnL >= minRealizedPnL
	monthlyProfitAmountOK := f.MonthlyEntryNotional >= minEntryNotional && f.MonthlyRealizedPnL >= minRealizedPnL
	weeklyProfitSourceOK := isLuckySpikeProfitSource(f.WeeklyProfitSource)
	monthlyProfitSourceOK := isLuckySpikeProfitSource(f.MonthlyProfitSource)
	weeklyProfitOK := f.WeeklyProfitPctKnown && weeklyProfitSourceOK && f.WeeklyProfitPct > minProfitPct && weeklyProfitAmountOK
	monthlyProfitOK := f.MonthlyProfitPctKnown && monthlyProfitSourceOK && f.MonthlyProfitPct > minProfitPct && monthlyProfitAmountOK
	profitOK := weeklyProfitOK || monthlyProfitOK

	if frequencyOK {
		if weeklyStrictFrequencyOK {
			reasons = appendUnique(reasons, "WEEKLY_HIGH_FREQUENCY")
		}
		if monthlyStrictFrequencyOK {
			reasons = appendUnique(reasons, "MONTHLY_HIGH_FREQUENCY")
		}
		if weeklyObservedFrequencyOK {
			reasons = appendUnique(reasons, "WEEKLY_OBSERVED_HIGH_FREQUENCY")
		}
		if monthlyObservedFrequencyOK {
			reasons = appendUnique(reasons, "MONTHLY_OBSERVED_HIGH_FREQUENCY")
		}
	} else {
		reasons = appendUnique(reasons, "WEEKLY_FREQUENCY_TOO_LOW")
	}
	if strictCoverageOK {
		reasons = appendUnique(reasons, "WEEKLY_SUSTAINED_ACTIVITY")
	} else if observedCoverageOK {
		reasons = appendUnique(reasons, "OBSERVED_SUSTAINED_ACTIVITY")
	} else {
		reasons = appendUnique(reasons, "WEEKLY_SPAN_TOO_SHORT")
	}
	if (weeklyObservedFrequencyOK || monthlyObservedFrequencyOK) && (f.TradeHistoryPartialHint != "" || strings.HasPrefix(f.DataQuality, "partial_")) {
		reasons = appendUnique(reasons, "PARTIAL_HISTORY_LOWER_BOUND")
	}
	if cyclesOK {
		if positionSample >= minCycles {
			reasons = appendUnique(reasons, "POLYMARKET_POSITION_SAMPLE_OK")
		} else {
			reasons = appendUnique(reasons, "WEEKLY_REALIZED_SAMPLE_OK")
		}
	} else {
		reasons = appendUnique(reasons, "WEEKLY_REALIZED_SAMPLE_SMALL")
	}
	if weeklyProfitOK {
		reasons = appendUnique(reasons, "WEEKLY_PROFIT_PCT_ABOVE_30PCT")
	}
	if monthlyProfitOK {
		reasons = appendUnique(reasons, "MONTHLY_PROFIT_PCT_ABOVE_30PCT")
	}
	if profitOK {
		reasons = appendUnique(reasons, "WINDOW_PROFIT_PCT_ABOVE_30PCT")
	} else {
		reasons = appendUnique(reasons, "WINDOW_PROFIT_PCT_TOO_LOW")
	}
	reasons = appendUnique(reasons, "NOT_LEGAL_INSIDER_CLAIM")

	if sampleCount == 0 {
		missing = appendUnique(missing, "MISSING_POLYMARKET_POSITION_SAMPLE")
	}
	if maxInt(f.WeeklyTradeCount, f.MonthlyTradeCount) == 0 {
		missing = appendUnique(missing, "MISSING_WEEKLY_TRADES")
	}
	if !f.WeeklyProfitPctKnown && !f.MonthlyProfitPctKnown {
		missing = appendUnique(missing, "MISSING_WINDOW_PROFIT_PCT")
	}

	weeklyFreqRatio := min1(float64(f.WeeklyTradeCount) / float64(maxInt(1, minTrades)))
	monthlyFreqRatio := min1(float64(f.MonthlyTradeCount) / float64(maxInt(1, minTradesMonth)))
	weeklyObservedFreqRatio := 0.0
	if weeklyIntervalOK && f.WeeklyCoverage >= minObservedCoverage {
		weeklyObservedFreqRatio = min1(float64(f.WeeklyTradeCount) / float64(maxInt(1, minObservedTrades)))
	}
	monthlyObservedFreqRatio := 0.0
	if monthlyIntervalOK && f.MonthlyCoverage >= minObservedCoverage {
		monthlyObservedFreqRatio = min1(float64(f.MonthlyTradeCount) / float64(maxInt(1, minObservedTrades)))
	}
	freqRatio := maxFloat(maxFloat(weeklyFreqRatio, monthlyFreqRatio), maxFloat(weeklyObservedFreqRatio, monthlyObservedFreqRatio))
	if weeklyObservedFrequencyOK || monthlyObservedFrequencyOK {
		freqRatio = 1
	}
	profitRatio := 0.0
	if f.WeeklyProfitPctKnown && weeklyProfitAmountOK {
		profitRatio = maxFloat(profitRatio, min1((f.WeeklyProfitPct-minProfitPct)/0.30))
	}
	if f.MonthlyProfitPctKnown && monthlyProfitAmountOK {
		profitRatio = maxFloat(profitRatio, min1((f.MonthlyProfitPct-minProfitPct)/0.30))
	}
	cycleRatio := min1(float64(sampleCount) / float64(maxInt(1, minCycles*2)))
	coverageRatio := 0.0
	if minCoverage > 0 {
		coverageRatio = min1(maxFloat(float64(f.WeeklyCoverage), float64(f.MonthlyCoverage)) / float64(minCoverage))
	}
	if minObservedCoverage > 0 && observedCoverageOK {
		coverageRatio = maxFloat(coverageRatio, min1(maxFloat(float64(f.WeeklyCoverage), float64(f.MonthlyCoverage))/float64(minObservedCoverage)))
	}

	scoreF := 50*freqRatio + 35*profitRatio + 15*cycleRatio
	if coverageOK {
		scoreF += 5
	}
	score := int(math.Round(scoreF))
	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}

	conf := 0.20 + 0.35*freqRatio + 0.25*cycleRatio + 0.20*coverageRatio
	switch f.DataQuality {
	case "complete":
		// no penalty
	case "partial_offset_cap":
		conf *= 0.90
	case "partial_safety_cap":
		conf *= 0.92
	case "partial_local_cap":
		conf *= 0.88
	case "proxy":
		conf *= 0.80
	case "missing":
		conf *= 0.60
	default:
		if f.TradeHistoryPartialHint != "" {
			conf *= 0.90
		}
	}
	conf = clampFloat(conf, 0, 1)

	hardPass := frequencyOK && coverageOK && profitOK
	if !hardPass && conf >= minConf {
		conf = minConf - 0.01
	}
	promote := hardPass && score >= minScore && conf >= minConf
	if promote {
		reasons = appendUnique(reasons, "SUSPECTED_LUCK_SPIKE_PATTERN")
	}

	snap := FeatureSnapshot{
		"score_version":                        ScoreVersion,
		"lookback_hours":                       lookback.Hours(),
		"weekly_trade_count":                   f.WeeklyTradeCount,
		"weekly_distinct_markets":              f.WeeklyDistinctMarkets,
		"weekly_realized_cycles":               f.WeeklyRealizedCycles,
		"weekly_profitable_cycles":             f.WeeklyProfitableCycles,
		"weekly_losing_cycles":                 f.WeeklyLosingCycles,
		"weekly_realized_pnl":                  f.WeeklyRealizedPnL,
		"weekly_entry_notional":                f.WeeklyEntryNotional,
		"weekly_profit_pct":                    f.WeeklyProfitPct,
		"weekly_profit_pct_known":              f.WeeklyProfitPctKnown,
		"weekly_profit_source":                 f.WeeklyProfitSource,
		"weekly_profit_positions":              f.WeeklyProfitPositions,
		"monthly_trade_count":                  f.MonthlyTradeCount,
		"monthly_realized_cycles":              f.MonthlyRealizedCycles,
		"monthly_coverage_hours":               f.MonthlyCoverage.Hours(),
		"monthly_avg_trade_interval_minutes":   monthlyAvgInterval.Minutes(),
		"monthly_realized_pnl":                 f.MonthlyRealizedPnL,
		"monthly_entry_notional":               f.MonthlyEntryNotional,
		"monthly_profit_pct":                   f.MonthlyProfitPct,
		"monthly_profit_pct_known":             f.MonthlyProfitPctKnown,
		"monthly_profit_source":                f.MonthlyProfitSource,
		"monthly_profit_positions":             f.MonthlyProfitPositions,
		"weekly_coverage_hours":                f.WeeklyCoverage.Hours(),
		"weekly_avg_trade_interval_minutes":    weeklyAvgInterval.Minutes(),
		"weekly_avg_trade_notional":            f.WeeklyAvgTradeNotional,
		"data_quality":                         f.DataQuality,
		"trade_history_partial_hint":           f.TradeHistoryPartialHint,
		"min_profit_pct":                       minProfitPct,
		"min_trades_per_week":                  minTrades,
		"min_trades_per_month":                 minTradesMonth,
		"min_observed_trades":                  minObservedTrades,
		"min_observed_coverage_hours":          minObservedCoverage.Hours(),
		"min_entry_notional":                   minEntryNotional,
		"min_realized_pnl":                     minRealizedPnL,
		"min_realized_cycles":                  minCycles,
		"min_coverage_hours":                   minCoverage.Hours(),
		"max_avg_trade_interval_minutes":       maxInterval.Minutes(),
		"frequency_gate_pass":                  frequencyOK,
		"weekly_frequency_gate_pass":           weeklyFrequencyOK,
		"monthly_frequency_gate_pass":          monthlyFrequencyOK,
		"weekly_strict_frequency_gate_pass":    weeklyStrictFrequencyOK,
		"monthly_strict_frequency_gate_pass":   monthlyStrictFrequencyOK,
		"weekly_observed_frequency_gate_pass":  weeklyObservedFrequencyOK,
		"monthly_observed_frequency_gate_pass": monthlyObservedFrequencyOK,
		"coverage_gate_pass":                   coverageOK,
		"sample_gate_pass":                     cyclesOK,
		"profit_pct_gate_pass":                 profitOK,
		"weekly_profit_pct_gate_pass":          weeklyProfitOK,
		"monthly_profit_pct_gate_pass":         monthlyProfitOK,
		"weekly_profit_source_gate_pass":       weeklyProfitSourceOK,
		"monthly_profit_source_gate_pass":      monthlyProfitSourceOK,
		"weekly_profit_amount_gate_pass":       weeklyProfitAmountOK,
		"monthly_profit_amount_gate_pass":      monthlyProfitAmountOK,
	}
	if !f.LastTradeAt.IsZero() {
		snap["last_trade_at"] = f.LastTradeAt.UTC().Format("2006-01-02T15:04:05Z")
	}

	return ScoreResult{
		Strategy:        "lucky_spike_score",
		Class:           "insider_like",
		Score:           score,
		Confidence:      conf,
		Promote:         promote,
		ReasonCodes:     reasons,
		MissingData:     missing,
		FeatureSnapshot: snap,
		ScoreVersion:    ScoreVersion,
	}
}

func isLuckySpikeProfitSource(source string) bool {
	switch source {
	case "profile_pnl_delta", "positions_cash_pnl":
		return true
	default:
		return false
	}
}

func min1(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func maxFloat(a, b float64) float64 {
	if a >= b {
		return a
	}
	return b
}
