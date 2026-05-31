package walletintel

// SharkVetoResult captures whether a veto fired and why.
type SharkVetoResult struct {
	Fired  bool
	Reason string
}

// SharkVetoConfig holds the configurable thresholds for veto gates.
// Zero values use the defaults documented below.
type SharkVetoConfig struct {
	// NegativeProfilePnLThreshold: profile cashPnL <= this (negative) → veto.
	// Default: -50_000. Example: -50000 means "reject if all-time P&L is below -$50k".
	NegativeProfilePnLThreshold float64

	// MassiveNegativeProfilePnLThreshold: stricter threshold for hard-reject.
	// Default: -500_000.
	MassiveNegativeProfilePnLThreshold float64

	// MaxOpenPositionRatio: if open/total > this AND cycles < MinCyclesForRatio → veto.
	// Default: 0.80.
	MaxOpenPositionRatio float64

	// MinCyclesForRatioVeto: if cyclesCount >= this, the sample is considered
	// sufficient even if the open-position ratio is high.
	// Default: 30. A wallet with ≥30 closed/realized cycles has demonstrated
	// enough trading activity to score even with many unresolved positions.
	MinCyclesForRatioVeto int
}

func vetoDefaults(v SharkVetoConfig) SharkVetoConfig {
	// The Polymarket data-api returns ~100 positions by default regardless of
	// wallet size. For wallets with thousands of positions this is only a 2–5%
	// sample, making cashPnL unreliable for borderline cases. To avoid false
	// positives the standalone profile-PnL veto thresholds are calibrated to
	// levels where even a partial sample clearly signals a massive loser:
	if v.NegativeProfilePnLThreshold == 0 {
		v.NegativeProfilePnLThreshold = -500_000 // -$500k: notable loss from partial sample
	}
	if v.MassiveNegativeProfilePnLThreshold == 0 {
		v.MassiveNegativeProfilePnLThreshold = -2_000_000 // -$2M: unambiguous from any sample
	}
	if v.MaxOpenPositionRatio == 0 {
		v.MaxOpenPositionRatio = 0.80
	}
	if v.MinCyclesForRatioVeto == 0 {
		v.MinCyclesForRatioVeto = 30
	}
	return v
}

// CheckProfilePnLVeto fires when the wallet's all-time profile P&L (cashPnL =
// realized + unrealized, matching the Polymarket UI) is strongly negative.
// Two severity levels: NEGATIVE_ALL_TIME_PNL (>=-50k) and
// MASSIVE_NEGATIVE_ALL_TIME_PNL (<=-500k).
func CheckProfilePnLVeto(profileCashPnL float64, known bool, cfg SharkVetoConfig) SharkVetoResult {
	if !known {
		return SharkVetoResult{}
	}
	cfg = vetoDefaults(cfg)
	if profileCashPnL <= cfg.MassiveNegativeProfilePnLThreshold {
		return SharkVetoResult{Fired: true, Reason: "MASSIVE_NEGATIVE_ALL_TIME_PNL"}
	}
	if profileCashPnL <= cfg.NegativeProfilePnLThreshold {
		return SharkVetoResult{Fired: true, Reason: "NEGATIVE_ALL_TIME_PNL"}
	}
	return SharkVetoResult{}
}

// CheckProfileContradictionVeto fires when local scoring shows positive PnL
// but the global profile P&L (all-time cashPnL) is significantly negative —
// indicating the local sample is cherry-picking a profitable window while the
// wallet is a net loser overall.
func CheckProfileContradictionVeto(profileCashPnL float64, known bool, localPnL float64, cfg SharkVetoConfig) SharkVetoResult {
	if !known || profileCashPnL >= 0 || localPnL <= 0 {
		return SharkVetoResult{}
	}
	cfg = vetoDefaults(cfg)
	absDiff := -profileCashPnL // magnitude of the negative profile P&L
	if absDiff >= -cfg.MassiveNegativeProfilePnLThreshold {
		return SharkVetoResult{Fired: true, Reason: "PROFILE_PNL_CONTRADICTS_LOCAL_MASSIVE"}
	}
	if absDiff >= -cfg.NegativeProfilePnLThreshold && absDiff > localPnL {
		return SharkVetoResult{Fired: true, Reason: "PROFILE_PNL_CONTRADICTS_LOCAL"}
	}
	return SharkVetoResult{}
}

// CheckMissingRiskMetricsVeto fires when:
//   - profit_factor is unavailable (API path, not realized path), AND
//   - wallet has zero reconstructed realized cycles (no per-cycle win/loss data), AND
//   - data_quality is explicitly marked partial/proxy/missing, AND
//   - the API closed-position sample itself is small (< MinCyclesForRatioVeto).
//
// The last condition protects wallets like those with 450 closed positions —
// even without profit_factor, 450 positions is substantial API evidence. This
// veto targets the Radiant-Birdhouse class: small closed sample (25 positions)
// + no realized trading evidence + acknowledged partial data.
func CheckMissingRiskMetricsVeto(profitFactorAvail bool, realizedCyclesCount int, dataQuality string, closedPositionsCyclesCount int, cfg SharkVetoConfig) SharkVetoResult {
	if profitFactorAvail || realizedCyclesCount > 0 {
		return SharkVetoResult{}
	}
	cfg = vetoDefaults(cfg)
	// If the API closed-position sample is large, the exit_rate proxy is
	// backed by sufficient evidence — no veto needed.
	if closedPositionsCyclesCount >= cfg.MinCyclesForRatioVeto {
		return SharkVetoResult{}
	}
	// Only fire on explicitly partial/proxy/missing data quality — not on ""
	// (empty DataQuality is used in tests and "complete-but-not-labelled" cases).
	switch dataQuality {
	case "partial_offset_cap", "partial_safety_cap", "partial_local_cap", "proxy", "missing":
		return SharkVetoResult{Fired: true, Reason: "MISSING_RISK_QUALITY_METRICS_PARTIAL_DATA"}
	}
	return SharkVetoResult{}
}

// CheckSampleRatioVeto fires when the realized/closed-position sample is tiny
// relative to the wallet's total known position universe. A wallet with 1718
// positions where only 25 are closed (1.45%) cannot be safely promoted as an
// active shark on the basis of those 25 closed positions alone.
//
// Gate: open/total > MaxOpenPositionRatio AND cyclesCount < MinCyclesForRatioVeto.
func CheckSampleRatioVeto(openCount, totalCount, cyclesCount int, cfg SharkVetoConfig) SharkVetoResult {
	if totalCount <= 0 || openCount <= 0 {
		return SharkVetoResult{}
	}
	cfg = vetoDefaults(cfg)
	ratio := float64(openCount) / float64(totalCount)
	if ratio >= cfg.MaxOpenPositionRatio && cyclesCount < cfg.MinCyclesForRatioVeto {
		return SharkVetoResult{Fired: true, Reason: "REALIZED_SAMPLE_TOO_SMALL_FOR_POSITION_UNIVERSE"}
	}
	return SharkVetoResult{}
}

// ApplySharkVetoes runs all four promotion vetoes and returns (vetoed, vetoReason,
// extraReasonCodes). When any veto fires, promote is forced to false regardless
// of how the hard gates scored. All fired veto reason codes are appended.
func ApplySharkVetoes(f WalletFacts, promote bool, profitFactorAvail bool, cyclesCount int, totalPnL float64, cfg SharkVetoConfig) (newPromote bool, vetoReason string, extraReasons []string) {
	newPromote = promote

	check := func(v SharkVetoResult) {
		if !v.Fired {
			return
		}
		extraReasons = appendUnique(extraReasons, v.Reason)
		if newPromote {
			newPromote = false
			vetoReason = v.Reason
		}
	}

	check(CheckProfilePnLVeto(f.ProfileCashPnL, f.ProfileCashPnLKnown, cfg))
	check(CheckProfileContradictionVeto(f.ProfileCashPnL, f.ProfileCashPnLKnown, totalPnL, cfg))
	check(CheckMissingRiskMetricsVeto(profitFactorAvail, f.RealizedCyclesCount, f.DataQuality, cyclesCount, cfg))
	check(CheckSampleRatioVeto(f.HistoricalOpenPositionCount, f.HistoricalTotalPositionCount, cyclesCount, cfg))

	return newPromote, vetoReason, extraReasons
}
