package walletintel

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// Runner builds WalletFacts from data-api responses and runs the v4 scoring
// strategies. Persists scores + watchlist promotion deterministically.
type Runner struct {
	DataAPI          *dataapi.Client
	Store            *postgres.Store
	Log              *slog.Logger
	SharkParams      SharkParams
	InsiderParams    InsiderParams
	TargetCategories []string

	// LifetimeProbeLimit caps the number of trades pulled to derive the
	// lifetime trade count used by insider-like gates (default 50). The
	// goal is "does this wallet have <=10 lifetime trades?", not full
	// trade-by-trade analytics; full pagination is delegated to the
	// HistoryBackfillWorker.
	LifetimeProbeLimit int

	// Cooldown gate. nil-safe — disabled when not set.
	Cooldown *ScoreCooldown

	// Admin discovery alert emitter. nil-safe.
	Discovery *DiscoveryEmitter
}

// ScoreWallet runs the full scoring pipeline for a known wallet.
// newBet may be nil; insider scoring requires it for promotion.
//
// Cooldown: when r.Cooldown is non-nil and the wallet was scored under the
// current ScoreVersion within the TTL, the call is suppressed UNLESS one of:
//   - newBet != nil (event-driven, must always run)
//   - cooldown gate says the entry is stale or version-mismatched
//
// Callers that have stronger signals (backfill completed, new large trade)
// can call ScoreWalletForce to bypass the gate.
func (r *Runner) ScoreWallet(ctx context.Context, walletID, proxy string, newBet *NewBetContext, marketRules MarketRulesInputs, hasCluster bool, clusterScore int) (FinalDecision, error) {
	if newBet == nil && r.Cooldown != nil {
		if allowed, reason := r.Cooldown.Allow(walletID, ScoreVersion, false); !allowed {
			metrics.Inc("wallet_scoring_skipped_total{reason=" + reason + "}")
			if r.Log != nil {
				r.Log.Debug("scoring skipped cooldown",
					"wallet", proxy,
					"reason", reason)
			}
			return FinalDecision{FinalClass: "rejected"}, nil
		}
	}
	return r.scoreWalletImpl(ctx, walletID, proxy, newBet, marketRules, hasCluster, clusterScore)
}

// ScoreWalletForce bypasses the cooldown gate. Use only when there is a
// material new signal (backfill completed, new large trade, version bump).
func (r *Runner) ScoreWalletForce(ctx context.Context, walletID, proxy string, newBet *NewBetContext, marketRules MarketRulesInputs, hasCluster bool, clusterScore int) (FinalDecision, error) {
	if r.Cooldown != nil {
		_, _ = r.Cooldown.Allow(walletID, ScoreVersion, true)
	}
	return r.scoreWalletImpl(ctx, walletID, proxy, newBet, marketRules, hasCluster, clusterScore)
}

func (r *Runner) scoreWalletImpl(ctx context.Context, walletID, proxy string, newBet *NewBetContext, marketRules MarketRulesInputs, hasCluster bool, clusterScore int) (FinalDecision, error) {
	facts, err := r.AssembleFacts(ctx, walletID, proxy)
	if err != nil {
		return FinalDecision{}, err
	}
	facts.NewBet = newBet
	facts.TargetCategories = r.TargetCategories

	shark := ScoreShark(facts, r.SharkParams)
	insider := ScoreInsiderLike(facts, r.InsiderParams)
	rules := EvaluateRulesRisk(marketRules)
	decision := ScoreArbitrate(ArbitrateInputs{
		Shark:        shark,
		Insider:      insider,
		RulesRisk:    rules,
		HasCluster:   hasCluster,
		ClusterScore: clusterScore,
	})

	if _, err := r.Store.InsertScore(ctx, walletID, toScoreRow(shark)); err != nil {
		if r.Log != nil {
			r.Log.Warn("persist shark score", "wallet", proxy, "err", err)
		}
		metrics.Inc("wallet_scores_total{strategy=shark,result=error}")
	} else {
		metrics.Inc("wallet_scores_total{strategy=shark,result=" + classifyResult(shark) + "}")
	}
	if _, err := r.Store.InsertScore(ctx, walletID, toScoreRow(insider)); err != nil {
		if r.Log != nil {
			r.Log.Warn("persist insider score", "wallet", proxy, "err", err)
		}
		metrics.Inc("wallet_scores_total{strategy=insider,result=error}")
	} else {
		metrics.Inc("wallet_scores_total{strategy=insider,result=" + classifyResult(insider) + "}")
	}
	metrics.Inc("scoring_runs_total")

	// Snapshot prior watchlist state so the discovery emitter can detect a
	// fresh transition into / out of active/watch_only.
	var prevWatch *postgres.WatchedWallet
	if r.Discovery != nil {
		if got, ok, _ := r.Store.GetWatchlistRow(ctx, walletID); ok {
			prevWatch = &got
		}
	}

	promo := BuildPromotion(walletID, decision, facts, time.Now())
	if err := r.Store.UpsertWatchlist(ctx, toWatchlistRow(promo)); err != nil {
		if r.Log != nil {
			r.Log.Warn("upsert watchlist", "wallet", proxy, "err", err)
		}
	}
	if r.Discovery != nil {
		_ = r.Discovery.EmitOnTransition(ctx, walletID, proxy, prevWatch, promo, shark, insider)
	}
	if decision.Promote {
		metrics.Inc("watchlist_promotions_total{class=" + decision.FinalClass + "}")
		switch decision.FinalClass {
		case "shark":
			path, _ := shark.FeatureSnapshot["promotion_path"].(string)
			metrics.Inc("shark_promotions_total{path=" + safePath(path) + "}")
		case "insider_like":
			path, _ := insider.FeatureSnapshot["promotion_path"].(string)
			metrics.Inc("insider_like_promotions_total{path=" + safePath(path) + "}")
		}
	}
	if promo.Status == "streak_broken" {
		metrics.Inc("insider_streak_broken_total")
	}

	if r.Log != nil {
		r.Log.Info("wallet scored",
			"wallet", proxy, "strategy", "shark_score",
			"class", shark.Class, "score", shark.Score,
			"confidence", round2(shark.Confidence),
			"promote", shark.Promote,
			"reasons", topReasons(shark.ReasonCodes, 4))
		r.Log.Info("wallet scored",
			"wallet", proxy, "strategy", "insider_like_score",
			"class", insider.Class, "score", insider.Score,
			"confidence", round2(insider.Confidence),
			"promote", insider.Promote,
			"reasons", topReasons(insider.ReasonCodes, 4))
		if decision.Promote {
			r.Log.Info("wallet promoted to watchlist",
				"wallet", proxy,
				"class", decision.FinalClass,
				"status", promo.Status,
				"score", decision.FinalScore,
				"confidence", round2(decision.FinalConfidence))
		} else {
			r.Log.Info("wallet rejected",
				"wallet", proxy,
				"strategy", "arbitration",
				"status", promo.Status,
				"reasons", topReasons(decision.ReasonCodes, 4))
		}
	}
	return decision, nil
}

func safePath(p string) string {
	if p == "" {
		return "none"
	}
	return p
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

func topReasons(rs []string, n int) []string {
	if len(rs) <= n {
		return rs
	}
	return rs[:n]
}

func classifyResult(r ScoreResult) string {
	if r.Promote {
		return "promoted"
	}
	return "rejected"
}

// AssembleFacts queries the wallet-summary endpoints and builds WalletFacts.
// v4 sources historical close stats from wallet_closed_position_latest (populated
// by HistoryBackfillWorker) and a bounded /trades probe for the lifetime
// count used by insider-like scoring. Open positions are NOT inputs for
// the shark gates; current holder/lifecycle state is NOT input here either.
func (r *Runner) AssembleFacts(ctx context.Context, walletID, proxy string) (WalletFacts, error) {
	now := time.Now()
	f := WalletFacts{
		WalletID:     walletID,
		Wallet:       proxy,
		Now:          now,
		MaxStaleDays: r.SharkParams.MaxStaleDays,
	}

	// --- Realized trading profitability (v4.1 primary signal) ---
	if rs, err := r.Store.GetRealizedStats(ctx, walletID); err == nil {
		f.RealizedCyclesCount = rs.Cycles
		f.RealizedProfitableCyclesCount = rs.ProfitableCycles
		f.RealizedLosingCyclesCount = rs.LosingCycles
		f.RealizedWinRate = rs.WinRate
		f.RealizedTotalPnL = rs.TotalRealizedPnL
		f.RealizedAvgNotional = rs.AvgRealizedNotional
		f.RealizedMedianNotional = rs.MedianRealizedNotional
		f.RealizedAvgROI = rs.AvgRealizedROI
		f.RealizedProfitFactor = rs.ProfitFactor
		f.RealizedMaxWin = rs.MaxRealizedWin
		f.RealizedMaxLoss = rs.MaxRealizedLoss
		if rs.LastExitAt != nil {
			f.LastRealizedExitAt = *rs.LastExitAt
		}
	}

	// --- Historical close stats (v4 shark inputs, secondary signal) ---
	if stats, err := r.Store.GetHistoricalCloseStats(ctx, walletID); err == nil {
		f.ClosedPositionsCountHist = stats.ClosedCount
		f.ProfitableClosedPositions = stats.ProfitableCount
		f.LosingClosedPositions = stats.LosingCount
		f.HistoricalTotalBoughtClosed = stats.TotalBoughtClosed
		f.HistoricalRealizedPnL = stats.TotalRealizedPnL
		f.HistoricalAvgClosedStake = stats.AvgClosedStake
		f.HistoricalMedianClosedStake = stats.MedianClosedStake
		f.HistoricalWinRate = stats.WinRate
		f.HistoricalROI = stats.ROI
		f.HistoricalMaxWin = stats.MaxWin
		f.HistoricalMaxLoss = stats.MaxLoss
		f.HistoricalPnLKnown = stats.ClosedCount > 0
		if stats.LastClosedAt != nil {
			f.LastClosedAt = *stats.LastClosedAt
		}
		f.InsiderStreakClean = stats.LosingCount == 0
	}
	if bf, ok, err := r.Store.GetBackfillRecord(ctx, walletID); err == nil && ok {
		f.ClosedPositionsComplete = bf.ClosedPositionsComplete
		f.TradesBackfillComplete = bf.TradesComplete
		f.LifetimeTradeCount = bf.TradesFetched
		if bf.RawStats != nil {
			if v, ok := bf.RawStats["data_quality"].(string); ok {
				f.DataQuality = v
			}
			if v, ok := bf.RawStats["trades_partial_reason"].(string); ok {
				f.TradesPartialReason = v
			}
			if v, ok := bf.RawStats["closed_positions_partial_reason"].(string); ok {
				f.ClosedPositionsPartialReason = v
			}
		}
	}

	// --- Bounded lifetime probe (insider-like inputs) ---
	if f.LifetimeTradeCount == 0 {
		limit := r.LifetimeProbeLimit
		if limit <= 0 {
			limit = 50
		}
		trades, _, err := r.DataAPI.GetTrades(ctx, proxy, "", false, limit)
		if err == nil {
			f.LifetimeTradeCount = len(trades)
			f.TotalTrades = len(trades)
			var lastTs, firstTs time.Time
			cats := map[string]int{}
			var samples []float64
			for _, t := range trades {
				ts := time.Unix(t.Timestamp.Int64(), 0)
				if lastTs.IsZero() || ts.After(lastTs) {
					lastTs = ts
				}
				if firstTs.IsZero() || ts.Before(firstTs) {
					firstTs = ts
				}
				cat := r.categoryFor(ctx, t.ConditionID)
				if cat != "" {
					cats[cat]++
				}
				notional := t.UsdcSize.Float64()
				if notional == 0 {
					notional = t.Size.Float64() * t.Price.Float64()
				}
				if notional > 0 {
					samples = append(samples, notional)
				}
			}
			f.LastTradeAt = lastTs
			f.FirstTradeAt = firstTs
			f.CategoryDistribution = cats
			f.TradeSizeSamples = samples
		}
	} else {
		// Use trade count from backfill; fetch a tiny page for last_trade_at.
		f.TotalTrades = f.LifetimeTradeCount
		trades, _, err := r.DataAPI.GetTrades(ctx, proxy, "", false, 1)
		if err == nil && len(trades) > 0 {
			f.LastTradeAt = time.Unix(trades[0].Timestamp.Int64(), 0)
		}
	}
	// Insider streak signals: prefer the realized-cycle counts when we have
	// reconstructed trades (cleanest trading-PnL signal). Fall back to the
	// API closed-position counts (also PnL-based; never outcome-based).
	if f.RealizedCyclesCount > 0 {
		f.LifetimeProfitableCount = f.RealizedProfitableCyclesCount
		f.LifetimeLosingCount = f.RealizedLosingCyclesCount
	} else {
		f.LifetimeProfitableCount = f.ProfitableClosedPositions
		f.LifetimeLosingCount = f.LosingClosedPositions
	}
	f.InsiderStreakClean = f.LifetimeLosingCount == 0

	// --- Profile / all-time P&L (cashPnL = realized + unrealized, same as UI) ---
	if sum, err := r.DataAPI.GetUserSummary(ctx, proxy); err == nil {
		f.TotalMarketsTraded = sum.TotalMarketsTraded
		f.RealizedPnL = sum.RealizedPnL
		f.RealizedPnLKnown = sum.RealizedPnLKnown
		f.ProfileCashPnL = sum.TotalCashPnL
		f.ProfileCashPnLKnown = sum.TotalCashPnLKnown
		f.ProfileCashPnLSampleCount = sum.TotalCashPnLSampleCount
	}

	// --- Position universe breadth (for sample-ratio veto) ---
	if sample, err := r.Store.GetDiscoverySampleStats(ctx, walletID); err == nil {
		f.HistoricalTotalPositionCount = sample.PositionsChecked
		f.HistoricalOpenPositionCount = sample.OpenUnresolvedCount
	}

	// --- Rolling 7d / 30d activity + profitability windows ---
	type windowOut struct {
		trades        *int
		coverage      *time.Duration
		avgInterval   *time.Duration
		cycles        *int
		pnl           *float64
		entryNotional *float64
		profitPct     *float64
		profitKnown   *bool
	}
	fillWindow := func(lookback time.Duration, out windowOut) {
		start := now.Add(-lookback)
		if ts, err := r.Store.GetTradeWindowStats(ctx, walletID, start); err == nil {
			*out.trades = ts.Trades
			if ts.FirstTrade != nil && ts.LastTrade != nil && ts.LastTrade.After(*ts.FirstTrade) {
				*out.coverage = ts.LastTrade.Sub(*ts.FirstTrade)
			}
			if ts.Trades > 0 {
				*out.avgInterval = time.Duration(float64(lookback) / float64(ts.Trades))
			}
		}
		if rs, err := r.Store.GetRealizedWindowStats(ctx, walletID, start); err == nil {
			*out.cycles = rs.Cycles
			*out.pnl = rs.TotalPnL
			*out.entryNotional = rs.TotalEntryStake
			if rs.TotalEntryStake > 0 {
				*out.profitPct = rs.TotalPnL / rs.TotalEntryStake
				*out.profitKnown = true
			}
		}
	}
	fillWindow(7*24*time.Hour, windowOut{
		trades:        &f.WeeklyTradeCount,
		coverage:      &f.WeeklyCoverage,
		avgInterval:   &f.WeeklyAvgTradeInterval,
		cycles:        &f.WeeklyRealizedCycles,
		pnl:           &f.WeeklyRealizedPnL,
		entryNotional: &f.WeeklyEntryNotional,
		profitPct:     &f.WeeklyProfitPct,
		profitKnown:   &f.WeeklyProfitPctKnown,
	})
	fillWindow(30*24*time.Hour, windowOut{
		trades:        &f.MonthlyTradeCount,
		coverage:      &f.MonthlyCoverage,
		avgInterval:   &f.MonthlyAvgTradeInterval,
		cycles:        &f.MonthlyRealizedCycles,
		pnl:           &f.MonthlyRealizedPnL,
		entryNotional: &f.MonthlyEntryNotional,
		profitPct:     &f.MonthlyProfitPct,
		profitKnown:   &f.MonthlyProfitPctKnown,
	})

	return f, nil
}

func (r *Runner) categoryFor(ctx context.Context, conditionID string) string {
	var cat string
	_ = r.Store.Pool.QueryRow(ctx, `
		SELECT COALESCE(e.category, '')
		FROM markets m LEFT JOIN events e ON e.id = m.event_id
		WHERE m.condition_id = $1
	`, conditionID).Scan(&cat)
	return strings.ToLower(cat)
}
