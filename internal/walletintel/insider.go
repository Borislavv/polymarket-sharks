package walletintel

import (
	"math"
	"strings"
)

// ScoreInsiderLike evaluates a candidate wallet+bet for suspicious
// informed-flow patterns. Independent from shark ROI/win-rate gates: a
// wallet that fails the shark historical gates may still be insider-like.
//
// v4 hard gates for initial capture (first-large-bet path):
//   - LifetimeTradeCount in [1, MaxLifetimeForCapture] (default 10)
//   - NewBet.Notional   >= MinNotionalUSD (default 20_000)
//   - NewBet.Price       <= 1/MinOdds     (default 3.0 → price <= 0.3333)
//   - NewBet.Direction   known (else MISSING_TRADE_DIRECTION, no user alert)
//   - Market is in HighImpactCategories OR NewBet.MarketIsHighImpact
//
// Streak continuation path:
//   - LifetimeTradeCount in [1, MaxLifetimeForCapture]
//   - InsiderStreakClean (zero losing closed positions)
//   - NewBet meets large-bet + high-odds criteria
//
// Any loss in lifetime closed positions sets InsiderStreakClean=false; the
// caller (insider lifecycle reconciler) is responsible for transitioning the
// wallet's status to 'streak_broken' on demotion. The scoring function itself
// just emits STREAK_BROKEN reason — promotion is suppressed in that case.
//
// Output language never claims legal insider trading; only "insider-like",
// "suspicious informed-flow candidate", "unusual conviction".
func ScoreInsiderLike(f WalletFacts, p InsiderParams) ScoreResult {
	snap := FeatureSnapshot{}
	var reasons []string
	var missing []string

	// Resolve defaults.
	maxLifetime := p.MaxLifetimeForCapture
	if maxLifetime <= 0 {
		maxLifetime = 10
	}
	minNotional := p.MinNotionalUSD
	if minNotional <= 0 {
		minNotional = 20_000
	}
	minOdds := p.MinOdds
	if minOdds <= 0 {
		minOdds = 3.0
	}
	maxPriceForOdds := 1.0 / minOdds

	snap["score_version"] = ScoreVersion
	snap["lifetime_trade_count"] = f.LifetimeTradeCount
	snap["lifetime_profitable_count"] = f.LifetimeProfitableCount
	snap["lifetime_losing_count"] = f.LifetimeLosingCount
	snap["insider_streak_clean"] = f.InsiderStreakClean
	if !f.FirstTradeAt.IsZero() {
		snap["first_trade_at"] = f.FirstTradeAt.UTC().Format("2006-01-02T15:04:05Z")
	}

	if f.NewBet == nil {
		// Neutral result: insider scoring is bet-centric, so without a
		// concrete NewBetContext we cannot say the wallet "failed" insider
		// gates. Emit explicit informational reason codes so downstream
		// consumers never see nil/empty reasons and never misattribute a
		// shark rejection to insider-like.
		reasons = appendUnique(reasons, "NO_BIG_BET_CONTEXT")
		reasons = appendUnique(reasons, "ROI_NOT_REQUIRED")
		reasons = appendUnique(reasons, "NOT_LEGAL_INSIDER_CLAIM")
		missing = appendUnique(missing, "MISSING_NEW_BET_CONTEXT")
		missing = appendUnique(missing, "MISSING_TRADE_DIRECTION")
		snap["evaluated_without_new_bet"] = true
		snap["promotion_path"] = "none"
		return ScoreResult{
			Strategy:        "insider_like_score",
			Class:           "insider_like",
			Score:           0,
			Confidence:      0,
			Promote:         false,
			ReasonCodes:     reasons,
			MissingData:     missing,
			FeatureSnapshot: snap,
			ScoreVersion:    ScoreVersion,
		}
	}
	b := f.NewBet

	// Gate: direction known.
	directionKnown := b.Direction != ""
	if !directionKnown {
		missing = appendUnique(missing, "MISSING_TRADE_DIRECTION")
		reasons = appendUnique(reasons, "MISSING_TRADE_DIRECTION")
	}

	// Gate: low-history wallet.
	lowHistory := f.LifetimeTradeCount >= 1 && f.LifetimeTradeCount <= maxLifetime
	switch {
	case f.LifetimeTradeCount == 0:
		reasons = appendUnique(reasons, "NEW_WALLET")
	case lowHistory:
		reasons = appendUnique(reasons, "LOW_LIFETIME_HISTORY")
	default:
		reasons = appendUnique(reasons, "MATURE_WALLET_NOT_INSIDER_LIKE")
	}

	// Reasons that are always emitted so insider scoring never produces
	// nil/empty reason_codes and downstream readers can distinguish
	// insider-namespace from shark-namespace at a glance.
	reasons = appendUnique(reasons, "ROI_NOT_REQUIRED")
	reasons = appendUnique(reasons, "NOT_LEGAL_INSIDER_CLAIM")

	// Bet size + odds gates.
	bigBet := b.Notional >= minNotional
	highOdds := b.Price > 0 && b.Price <= maxPriceForOdds
	if bigBet {
		reasons = appendUnique(reasons, "FIRST_LARGE_BET_20K")
		reasons = appendUnique(reasons, "LARGE_ANONYMOUS_CONVICTION")
	} else {
		reasons = appendUnique(reasons, "BELOW_NOTIONAL_THRESHOLD")
	}
	if highOdds {
		reasons = appendUnique(reasons, "HIGH_ODDS_3X")
	}

	// Market relevance.
	highImpact := b.MarketIsHighImpact || categoryMatches(b.MarketCategory, p.HighImpactCategories)
	if highImpact {
		reasons = appendUnique(reasons, "HIGH_IMPACT_MARKET")
	}

	// Streak state.
	cleanStreak := f.InsiderStreakClean && f.LifetimeLosingCount == 0
	if cleanStreak {
		reasons = appendUnique(reasons, "NO_LOSSES_YET")
		if f.LifetimeProfitableCount == 1 {
			reasons = appendUnique(reasons, "FIRST_WIN_CONFIRMED")
		}
		if f.LifetimeProfitableCount >= 1 {
			reasons = appendUnique(reasons, "WINNING_STREAK")
			if f.LifetimeProfitableCount >= 2 {
				reasons = appendUnique(reasons, "STREAK_CONTINUES")
			}
		}
	} else if f.LifetimeLosingCount > 0 {
		reasons = appendUnique(reasons, "STREAK_BROKEN")
	}

	// Resolution data availability.
	if f.LifetimeTradeCount > 0 && !f.HistoricalPnLKnown {
		missing = appendUnique(missing, "MISSING_RESOLUTION_DATA")
	}

	// ---- composite score (used for ranking; gates above are authoritative) ----
	score := insiderHistoricalScore(f, b, bigBet, highOdds, highImpact)
	snap["raw_score"] = score
	snap["notional_usd"] = b.Notional
	snap["price"] = b.Price
	snap["direction"] = string(b.Direction)
	if b.Price > 0 && b.Price <= 1 {
		o := 1.0 / b.Price
		snap["odds"] = o
		snap["payoff_if_win"] = b.Notional * o
	}
	snap["market_high_impact"] = highImpact

	hardBlock := !directionKnown ||
		!lowHistory ||
		!bigBet ||
		!highOdds ||
		!highImpact ||
		(!cleanStreak && f.LifetimeLosingCount > 0)

	promote := !hardBlock
	if promote {
		reasons = appendUnique(reasons, "INSIDER_LIKE_CANDIDATE")
	}

	conf := insiderConfidence(f, b, directionKnown, bigBet, highOdds, highImpact, cleanStreak)

	snap["promotion_path"] = pickIfStr(promote, "insider_first_big_bet", "none")
	snap["clean_streak"] = cleanStreak
	snap["streak_continues"] = cleanStreak && lowHistory && bigBet && highOdds

	return ScoreResult{
		Strategy:        "insider_like_score",
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

// insiderHistoricalScore is a soft ranking score for insider-like candidates
// — used to drive severity in arbitration, not as a hard gate.
func insiderHistoricalScore(f WalletFacts, b *NewBetContext, bigBet, highOdds, highImpact bool) int {
	if b == nil {
		return 0
	}
	s := 0.0
	if f.LifetimeTradeCount == 0 {
		s += 25
	} else if f.LifetimeTradeCount <= 3 {
		s += 22
	} else if f.LifetimeTradeCount <= 10 {
		s += 15
	}
	if bigBet {
		s += 20
		if b.Notional >= 50_000 {
			s += 10
		}
		if b.Notional >= 100_000 {
			s += 5
		}
	}
	if highOdds {
		s += 15
		if b.Price > 0 && b.Price <= 0.2 {
			s += 10
		} else if b.Price > 0 && b.Price <= 0.25 {
			s += 5
		}
	}
	if highImpact {
		s += 10
	}
	if f.InsiderStreakClean && f.LifetimeProfitableCount >= 1 {
		s += 5
	}
	if b.NearCatalyst {
		s += 5
	}
	if s > 100 {
		s = 100
	}
	if s < 0 {
		s = 0
	}
	return int(math.Round(s))
}

func insiderConfidence(f WalletFacts, b *NewBetContext, directionKnown, bigBet, highOdds, highImpact, cleanStreak bool) float64 {
	conf := 1.0
	if !directionKnown {
		conf *= 0.0
	}
	if !bigBet {
		conf *= 0.5
	}
	if !highOdds {
		conf *= 0.6
	}
	if !highImpact {
		conf *= 0.7
	}
	if !cleanStreak {
		conf *= 0.5
	}
	if f.LifetimeTradeCount > 0 && !f.HistoricalPnLKnown {
		conf *= 0.85
	}
	if conf < 0 {
		conf = 0
	}
	if conf > 1 {
		conf = 1
	}
	return conf
}

func categoryMatches(cat string, list []string) bool {
	if cat == "" || len(list) == 0 {
		return false
	}
	lc := strings.ToLower(cat)
	for _, x := range list {
		if strings.ToLower(strings.TrimSpace(x)) == lc {
			return true
		}
	}
	return false
}
