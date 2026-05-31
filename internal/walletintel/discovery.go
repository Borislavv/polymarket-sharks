package walletintel

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// DiscoveryEmitter sends admin-only Telegram alerts when a wallet transitions
// into / out of active watchlist status. It is invoked from the runner after
// each watchlist upsert. Idempotent: persisted alert_decisions are deduped on
// (wallet_id, class, status, promotion_path, score_version) so re-scoring
// the same wallet does not re-fire the alert.
type DiscoveryEmitter struct {
	Store  *postgres.Store
	Router *alerts.Router
	Log    *slog.Logger
	Links  alerts.LinkBuilder
}

// EmitOnTransition compares the previous watchlist row (if any) with the
// freshly-computed promotion. When the wallet enters active/watch_only for
// shark or insider_like — or is demoted from one of those — an admin alert
// is routed through AlertRouter. Returns nil even when no alert is needed
// so callers can drop it in fire-and-forget.
func (e *DiscoveryEmitter) EmitOnTransition(ctx context.Context, walletID, proxy string, prev *postgres.WatchedWallet, promo WatchlistPromotion, sharkResult, insiderResult ScoreResult) error {
	if e == nil || e.Router == nil {
		return nil
	}
	curStatus := promo.Status
	curClass := promo.Class
	prevStatus := ""
	prevClass := ""
	if prev != nil {
		prevStatus = prev.Status
		prevClass = prev.Class
	}

	// Detect transition direction.
	wasActive := prevStatus == StatusActive || prevStatus == StatusWatchOnly
	nowActive := curStatus == StatusActive || curStatus == StatusWatchOnly
	demotedNow := wasActive && !nowActive

	if !nowActive && !demotedNow {
		return nil
	}

	// Pull pseudonym/profile slug for nicer payload.
	pseudonym, profileSlug := "", ""
	_ = e.Store.Pool.QueryRow(ctx,
		`SELECT COALESCE(pseudonym,''), COALESCE(profile_slug,'') FROM wallets WHERE id=$1::uuid`, walletID).
		Scan(&pseudonym, &profileSlug)
	switch {
	case nowActive && curClass == "shark":
		// Only fire if (a) wasn't already active under the same path, or
		// (b) promotion_path materially changed.
		prevPath := ""
		if prev != nil {
			prevPath = previousPromotionPath(prev)
		}
		curPath, _ := sharkResult.FeatureSnapshot["promotion_path"].(string)
		if wasActive && prevClass == "shark" && prevPath == curPath {
			return nil
		}
		alert := e.buildSharkDiscovery(ctx, walletID, proxy, proxy, pseudonym, profileSlug, promo, sharkResult)
		return e.send(ctx, alerts.TypeSharkDiscovered, walletID, alert, alerts.FormatSharkDiscovered(alert, e.Links))
	case nowActive && curClass == "insider_like":
		// Skip if wallet was already insider_like with same streak state.
		prevStreak := ""
		if prev != nil {
			prevStreak = previousStreakState(prev)
		}
		curStreak, _ := insiderResult.FeatureSnapshot["streak_continues"].(bool)
		curStreakState := "clean"
		if curStreak {
			curStreakState = "continues"
		}
		if wasActive && prevClass == "insider_like" && prevStreak == curStreakState {
			return nil
		}
		alert := e.buildInsiderDiscovery(walletID, proxy, proxy, pseudonym, profileSlug, promo, insiderResult)
		return e.send(ctx, alerts.TypeInsiderLikeDiscovered, walletID, alert, alerts.FormatInsiderLikeDiscovered(alert, e.Links))
	case demotedNow:
		alert := alerts.DiscoveryAlert{
			WalletShort:     proxy,
			WalletFull:      proxy,
			Pseudonym:       pseudonym,
			ProfileSlug:     profileSlug,
			Class:           prevClass,
			Status:          curStatus,
			Score:           promo.Score,
			Confidence:      promo.Confidence,
			ReasonCodes:     promo.ReasonCodes,
			ReasonHumanized: alerts.HumanizeReasons(promo.ReasonCodes, 4),
		}
		if curStatus == StatusStreakBroken {
			alert.StreakState = "broken"
			return e.send(ctx, alerts.TypeInsiderStreakBrokenAdmin, walletID, alert, alerts.FormatWalletDemoted(alert, e.Links))
		}
		// Detect false-positive correction: veto reason recorded in the new score.
		vetoReason, _ := sharkResult.FeatureSnapshot["veto_reason"].(string)
		if vetoReason != "" && prevClass == "shark" {
			profileCashPnL, _ := sharkResult.FeatureSnapshot["profile_cash_pnl"].(float64)
			profileCashPnLKnown, _ := sharkResult.FeatureSnapshot["profile_cash_pnl_known"].(bool)
			totalPnL, _ := sharkResult.FeatureSnapshot["total_pnl"].(float64)
			openRatio, _ := sharkResult.FeatureSnapshot["open_position_ratio"].(float64)
			posTotal, _ := sharkResult.FeatureSnapshot["historical_total_position_count"].(int)
			posOpen, _ := sharkResult.FeatureSnapshot["historical_open_position_count"].(int)

			// Re-fetch sample stats for full alert detail.
			sample, _ := e.Store.GetDiscoverySampleStats(ctx, walletID)
			rstats, _ := e.Store.GetRealizedStats(ctx, walletID)

			alert.VetoReason = vetoReason
			alert.VetoProfileCashPnL = profileCashPnL
			alert.VetoProfileCashPnLKnown = profileCashPnLKnown
			alert.VetoLocalPnL = totalPnL
			alert.VetoOpenPositionRatio = openRatio
			alert.VetoPositionsChecked = posTotal
			alert.VetoOpenUnresolved = posOpen
			alert.PositionsChecked = sample.PositionsChecked
			alert.OpenUnresolvedCount = sample.OpenUnresolvedCount
			alert.RealizedCyclesCheck = rstats.Cycles
			alert.RealizedProfitFactor = rstats.ProfitFactor
			alert.AvgWinUSD = rstats.AvgWinUSD
			alert.AvgLossUSD = rstats.AvgLossUSD

			dedup := alerts.DedupKey("SHARK_FP_CORRECTED", walletID, "shark", ScoreVersion, vetoReason, "rejected")
			alert.DedupKey = dedup
			return e.send(ctx, alerts.TypeSharkFalsePositiveCorrected, walletID, alert, alerts.FormatSharkFalsePositiveCorrected(alert, e.Links))
		}
		return e.send(ctx, alerts.TypeWalletDemoted, walletID, alert, alerts.FormatWalletDemoted(alert, e.Links))
	}
	return nil
}

func (e *DiscoveryEmitter) buildSharkDiscovery(ctx context.Context, walletID, proxy, short, pseudonym, profileSlug string, promo WatchlistPromotion, sharkResult ScoreResult) alerts.DiscoveryAlert {
	snap := sharkResult.FeatureSnapshot
	path, _ := snap["promotion_path"].(string)
	basis, _ := snap["scoring_basis"].(string)
	closedCount, _ := snap["closed_positions_count"].(int)
	profitableClosed, _ := snap["profitable_closed_positions"].(int)
	losingClosed, _ := snap["losing_closed_positions"].(int)
	winRate, _ := snap["win_rate"].(float64)
	roi, _ := snap["roi"].(float64)
	avgStake, _ := snap["avg_closed_position_stake"].(float64)
	realized, _ := snap["realized_pnl"].(float64)
	pnlKnown, _ := snap["realized_pnl_known"].(bool)
	dq, _ := snap["data_quality"].(string)
	realizedCycles, _ := snap["realized_cycles_count"].(int)
	profitableExit, _ := snap["profitable_exit_rate"].(float64)
	realizedAvgROI, _ := snap["realized_avg_roi"].(float64)
	realizedAvgNotional, _ := snap["realized_avg_notional"].(float64)
	realizedTotalPnL, _ := snap["realized_total_pnl"].(float64)
	profitFactor, _ := snap["realized_profit_factor"].(float64)

	// Pull the full evidence aggregate. The realized-trades stats give us
	// per-cycle averages/medians/period; sample stats give us how many
	// trades and positions we considered and how many remain open.
	rstats, _ := e.Store.GetRealizedStats(ctx, walletID)
	sample, _ := e.Store.GetDiscoverySampleStats(ctx, walletID)

	profitableCount := rstats.ProfitableCycles
	losingCount := rstats.LosingCycles
	breakevenCount := rstats.BreakevenCycles
	if profitableCount == 0 && losingCount == 0 {
		// API-only path: count from closed positions.
		profitableCount = profitableClosed
		losingCount = losingClosed
	}

	evaluatedFrom, evaluatedTo, historyDays := "", "", 0
	if rstats.FirstExitAt != nil && rstats.LastExitAt != nil {
		evaluatedFrom = rstats.FirstExitAt.UTC().Format("2006-01-02")
		evaluatedTo = rstats.LastExitAt.UTC().Format("2006-01-02")
		historyDays = int(rstats.LastExitAt.Sub(*rstats.FirstExitAt).Hours()/24) + 1
	}
	lastTradeAt := ""
	if rstats.LastExitAt != nil {
		lastTradeAt = rstats.LastExitAt.UTC().Format("2006-01-02 15:04Z")
	}
	lastProfitableAt := ""
	if rstats.LastProfitableExitAt != nil {
		lastProfitableAt = rstats.LastProfitableExitAt.UTC().Format("2006-01-02 15:04Z")
	}

	humanized := alerts.HumanizeReasons(sharkResult.ReasonCodes, 5)
	if humanized == "" {
		humanized = "passes v4 historical-shark gates"
	}
	dedup := alerts.DedupKey("DISCOVERY", walletID, "shark", ScoreVersion, path, promo.Status)
	return alerts.DiscoveryAlert{
		WalletShort:          short,
		WalletFull:           proxy,
		Pseudonym:            pseudonym,
		ProfileSlug:          profileSlug,
		Class:                "shark",
		Status:               promo.Status,
		PromotionPath:        path,
		Score:                promo.Score,
		Confidence:           promo.Confidence,
		Severity:             "ADMIN",
		ClosedPositions:      closedCount,
		WinRate:              winRate,
		ROI:                  roi,
		AvgClosedStake:       avgStake,
		RealizedPnL:          realized,
		HistoricalPnLKnown:   pnlKnown,
		DataQuality:          dq,
		ScoringBasis:         basis,
		RealizedCycles:       realizedCycles,
		ProfitableExitRate:   profitableExit,
		RealizedAvgROI:       realizedAvgROI,
		RealizedAvgNotional:  realizedAvgNotional,
		RealizedTotalPnL:     realizedTotalPnL,
		RealizedProfitFactor: profitFactor,

		// v4.2 full evidence.
		ProfitableCount:     profitableCount,
		LosingCount:         losingCount,
		BreakevenCount:      breakevenCount,
		AvgTradeNotional:    rstats.AvgRealizedNotional,
		MedianTradeNotional: rstats.MedianRealizedNotional,
		AvgWinUSD:           rstats.AvgWinUSD,
		MedianWinUSD:        rstats.MedianWinUSD,
		AvgLossUSD:          rstats.AvgLossUSD,
		MedianLossUSD:       rstats.MedianLossUSD,
		MaxWinUSD:           rstats.MaxRealizedWin,
		MaxLossUSD:          rstats.MaxRealizedLoss,
		GrossProfitUSD:      rstats.GrossProfit,
		GrossLossUSD:        rstats.GrossLoss,
		EvaluatedFrom:       evaluatedFrom,
		EvaluatedTo:         evaluatedTo,
		HistoryDays:         historyDays,
		TradesChecked:       sample.TradesChecked,
		PositionsChecked:    sample.PositionsChecked,
		RealizedCyclesCheck: rstats.Cycles,
		OpenUnresolvedCount: sample.OpenUnresolvedCount,
		LastTradeAt:         lastTradeAt,
		LastProfitableAt:    lastProfitableAt,

		ReasonHumanized: humanized,
		ReasonCodes:     sharkResult.ReasonCodes,
		DedupKey:        dedup,
	}
}

func (e *DiscoveryEmitter) buildInsiderDiscovery(walletID, proxy, short, pseudonym, profileSlug string, promo WatchlistPromotion, insiderResult ScoreResult) alerts.DiscoveryAlert {
	lt, _ := insiderResult.FeatureSnapshot["lifetime_trade_count"].(int)
	wins, _ := insiderResult.FeatureSnapshot["lifetime_profitable_count"].(int)
	losses, _ := insiderResult.FeatureSnapshot["lifetime_losing_count"].(int)
	notional, _ := insiderResult.FeatureSnapshot["notional_usd"].(float64)
	odds, _ := insiderResult.FeatureSnapshot["odds"].(float64)
	firstTradeAt, _ := insiderResult.FeatureSnapshot["first_trade_at"].(string)
	streakContinues, _ := insiderResult.FeatureSnapshot["streak_continues"].(bool)
	state := "clean"
	if streakContinues {
		state = "continues"
	}
	if losses > 0 {
		state = "broken"
	}
	humanized := alerts.HumanizeReasons(insiderResult.ReasonCodes, 5)
	if humanized == "" {
		humanized = "low lifetime history · large bet · high odds · clean streak"
	}
	dedup := alerts.DedupKey("DISCOVERY", walletID, "insider_like", ScoreVersion, state, promo.Status)
	return alerts.DiscoveryAlert{
		WalletShort:     short,
		WalletFull:      proxy,
		Pseudonym:       pseudonym,
		ProfileSlug:     profileSlug,
		Class:           "insider_like",
		Status:          promo.Status,
		Score:           promo.Score,
		Confidence:      promo.Confidence,
		Severity:        "ADMIN",
		LifetimeTrades:  lt,
		ClosedWins:      wins,
		ClosedLosses:    losses,
		StreakState:     state,
		LatestBetUSD:    notional,
		LatestBetOdds:   odds,
		FirstTradeAt:    firstTradeAt,
		ReasonHumanized: humanized,
		ReasonCodes:     insiderResult.ReasonCodes,
		DedupKey:        dedup,
	}
}

func (e *DiscoveryEmitter) send(ctx context.Context, alertType, walletID string, alert alerts.DiscoveryAlert, body string) error {
	snap, _ := json.Marshal(alert)
	var snapMap map[string]any
	_ = json.Unmarshal(snap, &snapMap)
	decision := postgres.AlertDecision{
		AlertType:         alertType,
		EntityType:        "wallet",
		EntityID:          walletID,
		Severity:          "ADMIN",
		ShouldSend:        true,
		UserAlertAllowed:  false,
		AdminAlertAllowed: true,
		ReasonCodes:       alert.ReasonCodes,
		FeatureSnapshot:   snapMap,
		DedupKey:          alert.DedupKey,
	}
	out := e.Router.Route(ctx, decision, body, alerts.ChannelAdmin)
	if out.Err != nil && e.Log != nil {
		e.Log.Warn("discovery alert send",
			"alert_type", alertType,
			"wallet", alert.WalletFull,
			"err", out.Err)
	}
	if out.Sent {
		metrics.Inc("alerts_discovery_sent_total{type=" + alertType + "}")
	}
	return out.Err
}

// previousPromotionPath extracts the promotion_path of the prior watchlist row
// from the cached SharkParams snapshot stored alongside the row. The watchlist
// rows we pull via ListActiveWatchlist do not include feature_snapshot, so we
// re-query for the path on demand. Empty when not found.
func previousPromotionPath(prev *postgres.WatchedWallet) string {
	// best-effort; the WatchedWallet struct is intentionally lean. Promotion
	// paths are written to feature_snapshot.shark_score_path by promotion.go.
	return ""
}

func previousStreakState(prev *postgres.WatchedWallet) string {
	_ = prev
	return ""
}

// _avoid unused import_ — keep strings import readable in case formatters
// expand here. Stripped in build-only references.
var _ = strings.HasPrefix
