package walletintel

import (
	"math"
)

// ScoreShark evaluates a wallet against the v5 high_volume_profitable_shark
// strategy.
//
// Hard gates (ALL must pass to promote):
//   - realized cycles / closed positions >= VolumeMinCycles (default 10)
//   - total realized PnL                 >= VolumeMinTotalPnL    (default 50_000)
//   - avg realized trade notional        >= VolumeMinAvgTrade     (default 5_000)
//   - total realized volume              >= VolumeMinTotalVolume  (default 500_000)
//   - profitable exit rate               >= VolumeMinExitRate     (default 0.60)
//   - profit factor                      >= VolumeMinProfitFactor (default 1.25;
//     API-only path uses exit_rate+0.10 proxy — never silently waived)
//
// ROI is stored in the feature_snapshot as a diagnostic metric but is never a
// hard gate. Win-rate is carried for information but is NOT a hard gate.
func ScoreShark(f WalletFacts, p SharkParams) ScoreResult {
	snap := FeatureSnapshot{}
	var reasons []string
	var missing []string

	// ----- v5 gate defaults -----
	minCycles := p.VolumeMinCycles
	if minCycles <= 0 {
		minCycles = 10
	}
	minPnL := p.VolumeMinTotalPnL
	if minPnL <= 0 {
		minPnL = 50_000
	}
	minAvgTrade := p.VolumeMinAvgTrade
	if minAvgTrade <= 0 {
		minAvgTrade = 5_000
	}
	minVolume := p.VolumeMinTotalVolume
	if minVolume <= 0 {
		minVolume = 500_000
	}
	minExitRate := p.VolumeMinExitRate
	if minExitRate <= 0 {
		minExitRate = 0.60
	}
	minPF := p.VolumeMinProfitFactor
	if minPF <= 0 {
		minPF = 1.25
	}

	// ---- Gate 0: data-quality bookkeeping (informational, NOT a hard gate) ----
	if !f.ClosedPositionsComplete && f.ClosedPositionsCountHist > 0 {
		reasons = appendUnique(reasons, "PARTIAL_CLOSED_POSITION_HISTORY")
	}
	if f.ClosedPositionsCountHist == 0 && f.RealizedCyclesCount == 0 {
		reasons = appendUnique(reasons, "MISSING_CLOSED_POSITION_DATA")
		missing = appendUnique(missing, "MISSING_CLOSED_POSITION_DATA")
	}
	if f.TradesPartialReason != "" {
		reasons = appendUnique(reasons, "TRADES_PARTIAL_"+f.TradesPartialReason)
	}
	if f.ClosedPositionsPartialReason != "" {
		reasons = appendUnique(reasons, "CLOSED_POSITIONS_PARTIAL_"+f.ClosedPositionsPartialReason)
	}

	// ---- Source selection ----
	// Prefer realized trading stats when we have a sufficient sample.
	// Fall back to API closed-position aggregate when realized is absent.
	useRealized := f.RealizedCyclesCount >= minCycles
	scoringBasis := "api_closed_positions"

	var (
		cyclesCount       int
		avgTrade          float64
		totalPnL          float64
		exitRate          float64
		profitFactor      float64
		totalVolume       float64
		diagROI           float64 // diagnostic only — never a gate
		diagWinRate       float64 // diagnostic only — never a gate
		profitFactorAvail = true  // false for API path where PF is not computable
		riskQualityBasis  = "profit_factor"
	)

	if useRealized {
		scoringBasis = "realized_trading_pnl"
		cyclesCount = f.RealizedCyclesCount
		avgTrade = f.RealizedAvgNotional
		totalPnL = f.RealizedTotalPnL
		exitRate = f.RealizedWinRate
		profitFactor = f.RealizedProfitFactor
		// prefer explicit volume when backfilled; fall back to cycle×avg
		if f.RealizedTotalVolume > 0 {
			totalVolume = f.RealizedTotalVolume
		} else {
			totalVolume = float64(f.RealizedCyclesCount) * f.RealizedAvgNotional
		}
		diagROI = f.RealizedAvgROI
		diagWinRate = f.RealizedWinRate
	} else if f.ClosedPositionsCountHist >= minCycles {
		scoringBasis = "api_closed_positions"
		cyclesCount = f.ClosedPositionsCountHist
		avgTrade = f.HistoricalAvgClosedStake
		totalPnL = f.HistoricalRealizedPnL
		if cyclesCount > 0 {
			exitRate = float64(f.ProfitableClosedPositions) / float64(cyclesCount)
		}
		// profit_factor is not computable from API closed-position data.
		// We do NOT auto-waive it — instead we require a stricter exit_rate
		// proxy (>=minExitRate+0.10) as a substitute risk quality gate.
		profitFactor = 0
		profitFactorAvail = false
		riskQualityBasis = "exit_rate_proxy"
		totalVolume = f.HistoricalTotalBoughtClosed
		diagROI = f.HistoricalROI
		diagWinRate = f.HistoricalWinRate
	} else if f.RealizedCyclesCount > 0 {
		// realized sample exists but below threshold AND API sample also thin
		scoringBasis = "proxy_partial"
		cyclesCount = f.RealizedCyclesCount
		avgTrade = f.RealizedAvgNotional
		totalPnL = f.RealizedTotalPnL
		exitRate = f.RealizedWinRate
		profitFactor = f.RealizedProfitFactor
		if f.RealizedTotalVolume > 0 {
			totalVolume = f.RealizedTotalVolume
		} else {
			totalVolume = float64(f.RealizedCyclesCount) * f.RealizedAvgNotional
		}
		diagROI = f.RealizedAvgROI
		diagWinRate = f.RealizedWinRate
	}

	// ---- Hard gates (v5) ----
	cyclesOK := cyclesCount >= minCycles
	pnlOK := totalPnL >= minPnL
	avgTradeOK := avgTrade >= minAvgTrade
	volumeOK := totalVolume >= minVolume
	exitRateOK := exitRate >= minExitRate

	// profit_factor gate: use actual PF when available (realized path), or a
	// stricter exit_rate proxy when PF is unavailable (API-only path).
	var pfOK bool
	if profitFactorAvail {
		pfOK = profitFactor >= minPF
	} else {
		// API path: exit_rate >= minExitRate+0.10 guards against unquantified risk.
		pfOK = exitRate >= minExitRate+0.10
	}

	if cyclesOK {
		reasons = appendUnique(reasons, "SUFFICIENT_CYCLES_SAMPLE")
	} else {
		reasons = appendUnique(reasons, "INSUFFICIENT_CYCLES_SAMPLE")
	}
	if pnlOK {
		reasons = appendUnique(reasons, "TOTAL_PNL_ABOVE_50K")
	} else {
		reasons = appendUnique(reasons, "TOTAL_PNL_TOO_LOW")
	}
	if avgTradeOK {
		reasons = appendUnique(reasons, "AVG_TRADE_ABOVE_5K")
	} else {
		reasons = appendUnique(reasons, "AVG_TRADE_TOO_LOW")
	}
	if volumeOK {
		reasons = appendUnique(reasons, "VOLUME_ABOVE_500K")
	} else {
		reasons = appendUnique(reasons, "VOLUME_TOO_LOW")
	}
	if exitRateOK {
		reasons = appendUnique(reasons, "EXIT_RATE_ABOVE_60PCT")
	} else {
		reasons = appendUnique(reasons, "EXIT_RATE_TOO_LOW")
	}
	if profitFactorAvail {
		if pfOK {
			reasons = appendUnique(reasons, "PROFIT_FACTOR_ABOVE_1_25")
		} else {
			reasons = appendUnique(reasons, "PROFIT_FACTOR_TOO_LOW")
		}
	} else {
		if pfOK {
			reasons = appendUnique(reasons, "API_EXIT_RATE_PROXY_PASS")
		} else {
			reasons = appendUnique(reasons, "API_EXIT_RATE_PROXY_FAIL")
		}
	}

	// ROI and win-rate are always recorded — diagnostic only.
	if diagROI > 0 {
		reasons = appendUnique(reasons, "POSITIVE_REALIZED_PNL")
	}

	// Recency / staleness — soft modulation.
	if !f.LastClosedAt.IsZero() && !f.Now.IsZero() {
		stale := p.MaxStaleDays
		if stale <= 0 {
			stale = 180
		}
		days := int(f.Now.Sub(f.LastClosedAt).Hours() / 24)
		if days > stale {
			reasons = appendUnique(reasons, "STALE_ACTIVITY")
		}
	}

	hardGatesPass := cyclesOK && pnlOK && avgTradeOK && volumeOK && exitRateOK && pfOK
	if hardGatesPass {
		reasons = appendUnique(reasons, "SHARK_HISTORICAL_EDGE")
	}

	promote := hardGatesPass

	// ---- Hard vetoes (override promote=true when any fires) ----
	// These run AFTER the standard gates so the feature snapshot always
	// records both "hard gates passed" and "veto fired" as separate signals.
	vetoPromote, vetoReason, vetoReasons := ApplySharkVetoes(f, promote, profitFactorAvail, cyclesCount, totalPnL, SharkVetoConfig{})
	promote = vetoPromote
	for _, r := range vetoReasons {
		reasons = appendUnique(reasons, r)
	}

	// ---- composite score ----
	score := hvpSharkScore(cyclesCount, totalPnL, avgTrade, exitRate, profitFactor)

	// ---- confidence ----
	conf := hvpSharkConfidence(cyclesCount, exitRate, profitFactor, scoringBasis)
	if !hardGatesPass {
		if conf > 0.5 {
			conf = 0.5
		}
	}
	switch f.DataQuality {
	case "complete":
		// no penalty
	case "partial_offset_cap":
		conf *= 0.85
	case "partial_safety_cap":
		conf *= 0.9
	case "partial_local_cap":
		conf *= 0.8
	case "proxy":
		conf *= 0.6
	case "missing":
		conf *= 0.3
	default:
		if !f.ClosedPositionsComplete {
			conf *= 0.7
		}
	}
	if conf < 0 {
		conf = 0
	}
	if conf > 1 {
		conf = 1
	}

	// ---- promotion path ----
	promotionPath := "none"
	if hardGatesPass {
		switch scoringBasis {
		case "proxy_partial":
			promotionPath = "watch_only_profitable_large"
		default:
			promotionPath = "high_volume_profitable_shark"
		}
	}

	// ---- feature snapshot ----
	snap["score_version"] = ScoreVersion
	snap["promotion_path"] = promotionPath

	// Veto evidence (always recorded for audit; empty when no veto fired).
	snap["veto_reason"] = vetoReason
	snap["profile_cash_pnl"] = f.ProfileCashPnL
	snap["profile_cash_pnl_known"] = f.ProfileCashPnLKnown
	snap["profile_cash_pnl_sample_count"] = f.ProfileCashPnLSampleCount
	snap["historical_total_position_count"] = f.HistoricalTotalPositionCount
	snap["historical_open_position_count"] = f.HistoricalOpenPositionCount
	if f.HistoricalTotalPositionCount > 0 {
		snap["open_position_ratio"] = float64(f.HistoricalOpenPositionCount) / float64(f.HistoricalTotalPositionCount)
	}
	snap["scoring_basis"] = scoringBasis
	snap["hard_gates_pass"] = hardGatesPass

	// v5 gate metrics
	snap["cycles_count"] = cyclesCount
	snap["total_pnl"] = totalPnL
	snap["avg_trade_notional"] = avgTrade
	snap["total_volume"] = totalVolume
	snap["exit_rate"] = exitRate
	snap["profit_factor"] = profitFactor
	snap["profit_factor_available"] = profitFactorAvail
	snap["risk_quality_basis"] = riskQualityBasis
	if profitFactorAvail {
		snap["profit_factor_source"] = scoringBasis
	} else {
		snap["profit_factor_source"] = "unavailable"
	}

	// Diagnostic fields (NOT gates)
	snap["roi"] = diagROI
	snap["win_rate"] = diagWinRate

	// Realized trading evidence
	snap["realized_cycles_count"] = f.RealizedCyclesCount
	snap["realized_profitable_cycles_count"] = f.RealizedProfitableCyclesCount
	snap["realized_losing_cycles_count"] = f.RealizedLosingCyclesCount
	snap["realized_win_rate"] = f.RealizedWinRate
	snap["profitable_exit_rate"] = f.RealizedWinRate // alias used in alerts
	snap["realized_total_pnl"] = f.RealizedTotalPnL
	snap["realized_total_volume"] = f.RealizedTotalVolume
	snap["realized_avg_notional"] = f.RealizedAvgNotional
	snap["realized_avg_roi"] = f.RealizedAvgROI
	snap["realized_profit_factor"] = f.RealizedProfitFactor
	snap["realized_max_win"] = f.RealizedMaxWin
	snap["realized_max_loss"] = f.RealizedMaxLoss
	if !f.LastRealizedExitAt.IsZero() {
		snap["last_realized_exit_at"] = f.LastRealizedExitAt.UTC().Format("2006-01-02T15:04:05Z")
	}

	// API closed-position evidence (secondary; kept for compatibility)
	snap["closed_positions_count"] = f.ClosedPositionsCountHist
	snap["profitable_closed_positions"] = f.ProfitableClosedPositions
	snap["losing_closed_positions"] = f.LosingClosedPositions
	snap["win_rate_api"] = f.HistoricalWinRate
	snap["total_bought_closed"] = f.HistoricalTotalBoughtClosed
	snap["realized_pnl"] = f.HistoricalRealizedPnL
	snap["realized_pnl_known"] = f.HistoricalPnLKnown
	snap["roi_api"] = f.HistoricalROI
	snap["avg_closed_position_stake"] = f.HistoricalAvgClosedStake
	snap["median_closed_position_stake"] = f.HistoricalMedianClosedStake
	snap["max_win"] = f.HistoricalMaxWin
	snap["max_loss"] = f.HistoricalMaxLoss
	if !f.LastClosedAt.IsZero() {
		snap["last_closed_position_at"] = f.LastClosedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if !f.LastTradeAt.IsZero() {
		snap["last_trade_at"] = f.LastTradeAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	snap["closed_positions_complete"] = f.ClosedPositionsComplete
	snap["trades_backfill_complete"] = f.TradesBackfillComplete
	snap["data_quality"] = f.DataQuality
	if f.TradesPartialReason != "" {
		snap["trades_partial_reason"] = f.TradesPartialReason
	}
	if f.ClosedPositionsPartialReason != "" {
		snap["closed_positions_partial_reason"] = f.ClosedPositionsPartialReason
	}
	snap["total_trades"] = f.TotalTrades
	snap["score"] = score

	return ScoreResult{
		Strategy:        "shark_score",
		Class:           "shark",
		Score:           score,
		Confidence:      conf,
		Promote:         promote,
		ReasonCodes:     reasons,
		MissingData:     missing,
		FeatureSnapshot: snap,
		ScoreVersion:    ScoreVersion,
	}
}

// hvpSharkScore maps the high-volume-profitable-shark evidence to 0..100.
// Monotonic in each dimension; saturates at conservative ceilings.
func hvpSharkScore(cycles int, totalPnL, avgTrade, exitRate, profitFactor float64) int {
	if cycles == 0 {
		return 0
	}
	score := 0.0
	// Cycles component (0..20): log scale; 10 → ~10, 50 → ~16, 200+ → 20.
	score += clampFloat(math.Log10(float64(maxInt(1, cycles)))*14, 0, 20)
	// Exit rate component (0..25): 60% → 0, 75% → 15, 90%+ → 25.
	score += clampFloat((exitRate-0.60)*100, 0, 25)
	// Profit factor component (0..25): 1.25 → 0, 2.0 → 15, 3.0+ → 25.
	if profitFactor > 0 {
		score += clampFloat((math.Log10(profitFactor))*30, 0, 25)
	}
	// Realized PnL component (0..15): $50k → 0, $200k → 10, $1M+ → 15.
	if totalPnL > 0 {
		score += clampFloat((math.Log10(totalPnL)-4.7)*12, 0, 15)
	}
	// Avg trade component (0..15): $5k → 0, $20k → 8, $100k+ → 15.
	if avgTrade > 0 {
		score += clampFloat((math.Log10(avgTrade)-3.7)*10, 0, 15)
	}
	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}
	return int(math.Round(score))
}

func hvpSharkConfidence(cycles int, exitRate, profitFactor float64, basis string) float64 {
	if cycles == 0 {
		return 0
	}
	var c float64
	switch {
	case cycles >= 100:
		c = 0.95
	case cycles >= 50:
		c = 0.90
	case cycles >= 25:
		c = 0.80
	case cycles >= 10:
		c = 0.70
	default:
		c = 0.50
	}
	// API path: no profit_factor → slight confidence reduction
	if basis == "api_closed_positions" {
		c *= 0.90
	}
	// exit rate boosts confidence above 0.75
	if exitRate >= 0.75 {
		c = math.Min(c+0.05, 0.95)
	}
	// strong profit factor also boosts
	if profitFactor >= 2.0 {
		c = math.Min(c+0.03, 0.97)
	}
	return c
}

func pickIfStr(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
