package walletintel

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// WatchedWalletWorker polls /activity for watchlisted wallets, enriches via
// /trades when direction is missing, stores trades, creates watched_bets,
// and routes SHARK_BET / INSIDER_BET alerts via AlertRouter.
type WatchedWalletWorker struct {
	DataAPI       *dataapi.Client
	Store         *postgres.Store
	Router        *alerts.Router
	Log           *slog.Logger
	Interval      time.Duration
	Runner        *Runner
	InsiderParams InsiderParams
	Links         alerts.LinkBuilder

	// Lifecycle controls (passed from config).
	LifecycleEnabled       bool
	ExitAlertsEnabled      bool
	ExitFullCloseTolerance float64

	// Profit-tier gate (replaces simple SharkAlertMinNotionalUSD when Enabled).
	// Evaluates odds + profit_if_win against per-tier thresholds.
	ProfitGate ProfitGateParams

	// Legacy dust floor; used as fallback when ProfitGate.Enabled=false.
	// Bets below this notional are persisted but emit no user-facing alert.
	SharkAlertMinNotionalUSD float64
}

func (w *WatchedWalletWorker) Run(ctx context.Context) error {
	if w.Interval <= 0 {
		w.Interval = 60 * time.Second
	}
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	w.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			w.runOnce(ctx)
		}
	}
}

func (w *WatchedWalletWorker) runOnce(ctx context.Context) {
	start := time.Now()
	wallets, err := w.Store.ListActiveWatchlist(ctx)
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("watched wallet list", "err", err)
		}
		return
	}
	if w.Log != nil {
		w.Log.Info("watched wallet cycle started", "watched_wallets", len(wallets))
	}
	var activitiesFetched, newTrades, duplicates, opened, exits, alertsCreated int
	classCounts := map[string]int{}
	for _, ww := range wallets {
		classCounts[ww.Class]++
		activity, _, err := w.DataAPI.GetActivity(ctx, ww.ProxyWallet, 25)
		if err != nil {
			metrics.Inc("watched_errors_total")
			continue
		}
		activitiesFetched += len(activity)
		perWalletNew, perWalletDup, perWalletAlerts := 0, 0, 0
		var latestActivityTs time.Time
		skipReasons := map[string]int{}
		for _, a := range activity {
			ts := time.Unix(a.Timestamp.Int64(), 0)
			if ts.After(latestActivityTs) {
				latestActivityTs = ts
			}
			res := w.processActivity(ctx, ww, a)
			newTrades += res.NewTrade
			duplicates += res.Duplicate
			opened += res.Opened
			exits += res.Exit
			alertsCreated += res.AlertCreated
			perWalletNew += res.NewTrade
			perWalletDup += res.Duplicate
			perWalletAlerts += res.AlertCreated
			if res.SkipReason != "" {
				skipReasons[res.SkipReason]++
				metrics.Inc("watched_skip_total{reason=" + res.SkipReason + "}")
			}
		}
		if w.Log != nil {
			w.Log.Info("watched wallet cycle per-wallet",
				"wallet", ww.ProxyWallet,
				"class", ww.Class,
				"status", ww.Status,
				"activities", len(activity),
				"new_trades", perWalletNew,
				"duplicates", perWalletDup,
				"alerts_created", perWalletAlerts,
				"latest_activity_at", latestActivityTs.Format(time.RFC3339),
				"skip_reasons", skipReasons)
		}
	}
	if w.Log != nil {
		w.Log.Info("watched wallet cycle completed",
			"watched_wallets", len(wallets),
			"watched_by_class", classCounts,
			"activities_fetched", activitiesFetched,
			"new_trades", newTrades,
			"duplicates", duplicates,
			"opened_positions", opened,
			"exits_detected", exits,
			"alerts_created", alertsCreated,
			"duration", time.Since(start).String())
	}
}

// activityResult tallies what happened for one activity row. Returned from
// processActivity so the cycle summary stays accurate. SkipReason is set
// when an alert was suppressed and should be aggregated per-wallet.
type activityResult struct {
	NewTrade, Duplicate, Opened, Exit, AlertCreated int
	SkipReason                                      string
}

func (w *WatchedWalletWorker) processActivity(ctx context.Context, ww postgres.WatchedWallet, a dataapi.Activity) activityResult {
	var res activityResult

	// Non-TRADE activity types (REDEEM, SPLIT, etc.) carry no directional
	// side/outcome and are never bet candidates. Skip before direction attempt
	// so they don't pollute the unknown_direction counter.
	if t := strings.ToUpper(strings.TrimSpace(a.Type)); t != "" && t != "TRADE" {
		metrics.Inc("watched_skip_total{reason=non_trade_activity}")
		return res
	}

	outcome := strings.ToUpper(a.Outcome)
	side := strings.ToUpper(a.Side)
	if outcome == "" || side == "" {
		// enrich from /trades
		trades, _, err := w.DataAPI.GetTrades(ctx, ww.ProxyWallet, "", false, 20)
		if err == nil {
			for _, t := range trades {
				if t.TransactionHash == a.TransactionHash {
					outcome = strings.ToUpper(t.Outcome)
					side = strings.ToUpper(t.Side)
					break
				}
			}
		}
	}
	dirRes, dirErr := DirectionOf(outcome, side)
	if dirErr != nil {
		skipReason := "missing_fields"
		if w.Log != nil {
			w.Log.Warn("watched trade: direction missing",
				"wallet", ww.ProxyWallet,
				"tx_hash", a.TransactionHash,
				"outcome", outcome, "side", side, "type", a.Type, "err", dirErr)
		}
		metrics.Inc("watched_unknown_direction_total")
		res.SkipReason = skipReason
		return res
	}
	dir := dirRes.Direction
	dirOutcome := dirRes.DirectionOutcome

	// resolve market
	var marketID, marketSlug, marketQuestion, eventSlug, eventTitle, resolutionSource, rulesText string
	_ = w.Store.Pool.QueryRow(ctx, `
		SELECT m.id::text, COALESCE(m.slug,''), COALESCE(m.question,''),
		       COALESCE(e.slug,''), COALESCE(e.title,''),
		       COALESCE(m.resolution_source,''), COALESCE(m.rules_text,'')
		FROM markets m LEFT JOIN events e ON e.id = m.event_id
		WHERE m.condition_id = $1
	`, a.ConditionID).Scan(&marketID, &marketSlug, &marketQuestion, &eventSlug, &eventTitle, &resolutionSource, &rulesText)

	raw, _ := json.Marshal(a)
	priceF := a.Price.Float64()
	sizeF := a.Size.Float64()
	usdcF := a.UsdcSize.Float64()
	tradeID, inserted, err := w.Store.InsertWalletTrade(ctx, postgres.WalletTrade{
		TransactionHash: a.TransactionHash,
		WalletID:        ww.ID,
		MarketID:        marketID,
		ConditionID:     a.ConditionID,
		Outcome:         outcome,
		Side:            side,
		Direction:       string(dir),
		Price:           priceF,
		Size:            sizeF,
		UsdcSize:        usdcF,
		Timestamp:       time.Unix(a.Timestamp.Int64(), 0),
		Source:          "data-api/activity",
		Raw:             raw,
	})
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("insert wallet trade", "err", err)
		}
		return res
	}
	if !inserted {
		res.Duplicate = 1
		return res
	}
	res.NewTrade = 1
	metrics.Inc("watched_trades_inserted_total")
	metrics.Inc("watched_direction_total{direction=" + string(dir) + "}")
	if dir.IsCategorical() {
		metrics.Inc("watched_categorical_direction_total{side=" + side + "}")
	}

	notional := usdcF
	if notional == 0 {
		notional = sizeF * priceF
	}
	odds, _ := OddsFromPrice(priceF)
	payoff, _ := PayoffIfWin(priceF, notional)

	// Pre-classify entry vs exit so the watched_bets row carries the right
	// bet_kind from insertion. Exit is a SELL that matches an open lifecycle
	// row for this (wallet, condition, outcome).
	betKind := "entry"
	if w.LifecycleEnabled {
		if lc, ok, _ := w.Store.FindOpenLifecycle(ctx, ww.ID, a.ConditionID, outcome); ok {
			if IsExitSide(Direction(lc.OpenedDirection), side) {
				betKind = "exit"
			}
		}
	}

	betSnap := FeatureSnapshot{
		"direction":         string(dir),
		"direction_outcome": dirOutcome,
		"outcome":           outcome,
		"side":              side,
		"price":             priceF,
		"notional":          notional,
		"odds":              odds,
		"payoff_if_win":     payoff,
		"wallet_class":      ww.Class,
		"wallet_score":      ww.Score,
		"event_slug":        eventSlug,
		"market_slug":       marketSlug,
		"bet_kind":          betKind,
	}

	watchedBetID, err := w.Store.InsertWatchedBet(ctx, postgres.WatchedBet{
		WalletTradeID:    tradeID,
		WalletID:         ww.ID,
		MarketID:         marketID,
		Direction:        string(dir),
		Outcome:          outcome,
		DirectionOutcome: dirOutcome,
		Side:             side,
		Notional:         notional,
		Price:            priceF,
		Odds:             odds,
		PayoffIfWin:      payoff,
		WalletClass:      ww.Class,
		WalletScore:      ww.Score,
		ReasonCodes:      []string{strings.ToUpper(ww.Class) + "_NEW_BET"},
		FeatureSnapshot:  map[string]any(betSnap),
		DetectedAt:       time.Now(),
		BetKind:          betKind,
	})
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("insert watched bet", "err", err)
		}
		return res
	}
	metrics.Inc("watched_bets_inserted_total")

	if w.Log != nil {
		w.Log.Info("new watched bet",
			"wallet", ww.ProxyWallet,
			"class", ww.Class,
			"market_id", marketID,
			"direction", string(dir),
			"direction_outcome", dirOutcome,
			"outcome", outcome,
			"side", side,
			"notional", notional,
			"price", priceF,
			"tx_hash", a.TransactionHash)
	}

	// Lifecycle: is this an opening BUY or an exit SELL on an existing open position?
	exitHandled := false
	if w.LifecycleEnabled {
		exitHandled = w.handleLifecycle(ctx, ww, tradeID, a, outcome, side, priceF, sizeF, notional, marketID, marketSlug, marketQuestion, eventSlug)
	}
	if exitHandled {
		res.Exit = 1
		res.AlertCreated = 1
		return res
	}

	switch ww.Class {
	case "shark":
		// v4 entry-only: never emit SHARK_BET on a SELL (exit) trade.
		if betKind == "exit" {
			res.SkipReason = "exit_side"
			break
		}
		// score-version guard: stale watchlist row → admin-only diagnostic, no user alert.
		if ww.ScoreVersion != "" && ww.ScoreVersion != ScoreVersion {
			if w.Log != nil {
				w.Log.Info("shark alert suppressed",
					"reason", "stale_score_version",
					"wallet", ww.ProxyWallet,
					"score_version", ww.ScoreVersion)
			}
			metrics.Inc("shark_alerts_suppressed_stale_score_total")
			res.SkipReason = "stale_score_version"
			break
		}
		if w.routeSharkAlert(ctx, ww, watchedBetID, dir, dirOutcome, outcome, notional, priceF, odds, payoff, a, marketSlug, marketQuestion, eventSlug) {
			res.AlertCreated = 1
		}
	case "insider_like":
		// v4 entry-only + hard insider gates at alert time so a watched
		// wallet whose context degrades (loss appears, lifetime > 10, bet
		// notional below 20k, odds < 3, direction missing) gets no user
		// alert. We always persist the watched_bet row for audit.
		if betKind == "exit" {
			break
		}
		if w.routeInsiderAlertV4(ctx, ww, watchedBetID, dir, dirOutcome, notional, priceF, odds, payoff, a, marketSlug, marketQuestion, eventSlug, resolutionSource, rulesText, eventTitle) {
			res.AlertCreated = 1
		}
	}

	// Open lifecycle row for BUY (POC long-only model).
	if w.LifecycleEnabled && strings.ToUpper(side) == "BUY" && marketID != "" {
		_, _, err := w.Store.OpenLifecycle(ctx, postgres.LifecycleRow{
			WalletID:            ww.ID,
			MarketID:            marketID,
			ConditionID:         a.ConditionID,
			Outcome:             outcome,
			OpenedDirection:     string(dir),
			OpenedSide:          strings.ToUpper(side),
			OpenTradeID:         tradeID,
			OpenTransactionHash: a.TransactionHash,
			OpenNotional:        notional,
			OpenPrice:           priceF,
			OpenSize:            sizeF,
			OpenedAt:            time.Unix(a.Timestamp.Int64(), 0),
			FeatureSnapshot:     map[string]any{"class": ww.Class},
		})
		if err != nil && w.Log != nil {
			w.Log.Warn("open lifecycle", "err", err)
		}
		res.Opened = 1
		metrics.Inc("lifecycle_opened_total")
	}
	return res
}

// routeSharkAlert evaluates the profit-tier gate (or legacy dust floor) and
// routes a SHARK_BET alert. Always creates an alert_decision for audit.
// Returns true when a decision was created (pass or suppressed with audit record).
func (w *WatchedWalletWorker) routeSharkAlert(ctx context.Context, ww postgres.WatchedWallet, watchedBetID string, dir Direction, dirOutcome, outcome string, notional, price, odds, payoff float64, a dataapi.Activity, marketSlug, marketTitle, eventSlug string) bool {
	dedup := alerts.DedupKey("SHARK_BET", a.TransactionHash, ww.ProxyWallet, string(dir), a.ConditionID)

	// ── Profit-tier gate ──────────────────────────────────────────────────
	var gate ProfitGateResult
	gateEnabled := w.ProfitGate.Enabled

	if gateEnabled {
		gate = EvalProfitGate(notional, price, w.ProfitGate)

		// Metrics: count gate evaluations
		result := "pass"
		if !gate.Pass {
			result = "fail"
		}
		tierStr := string(gate.Tier)
		metrics.Inc("alert_profit_gate_total{class=shark,tier=" + tierStr + ",result=" + result + ",reason=" + gate.Reason + "}")
		metrics.Add("alert_profit_gate_profit_sum{class=shark,tier="+tierStr+"}", int64(gate.ProfitIfWin))
		metrics.Add("alert_profit_gate_notional_sum{class=shark,tier="+tierStr+"}", int64(notional))

		if !gate.Pass {
			if w.Log != nil {
				w.Log.Info("shark alert suppressed",
					"reason", gate.Reason,
					"wallet", ww.ProxyWallet,
					"notional", notional,
					"price", price,
					"odds", gate.Odds,
					"profit_if_win", gate.ProfitIfWin,
					"tier", string(gate.Tier),
					"required_odds", gate.MinOdds,
					"required_profit", gate.MinProfit)
			}
			metrics.Inc("shark_alerts_suppressed_profit_gate_total")

			// Persist suppressed decision for audit (ShouldSend=false).
			suppressedDecision := postgres.AlertDecision{
				AlertType:         alerts.TypeSharkBet,
				EntityType:        "watched_bet",
				EntityID:          watchedBetID,
				Severity:          "WARNING",
				ShouldSend:        false,
				UserAlertAllowed:  false,
				AdminAlertAllowed: false,
				ReasonCodes:       []string{"PROFIT_GATE_FAILED", gate.Reason},
				FeatureSnapshot: map[string]any{
					"wallet":              ww.ProxyWallet,
					"direction":           string(dir),
					"notional":            notional,
					"price":               price,
					"odds":                gate.Odds,
					"profit_if_win":       gate.ProfitIfWin,
					"payoff_if_win":       gate.PayoffIfWin,
					"score":               ww.Score,
					"confidence":          ww.Confidence,
					"alert_gate_enabled":  true,
					"alert_gate_tier":     string(gate.Tier),
					"alert_gate_pass":     false,
					"alert_gate_reason":   gate.Reason,
					"min_required_odds":   gate.MinOdds,
					"min_required_profit": gate.MinProfit,
				},
				DedupKey: dedup,
			}
			w.Router.Route(ctx, suppressedDecision, "", alerts.ChannelAdmin)
			return true
		}
	} else {
		// Legacy dust floor (backward compat when profit gate is disabled).
		minN := w.SharkAlertMinNotionalUSD
		if minN <= 0 {
			minN = 10_000
		}
		if notional < minN {
			if w.Log != nil {
				w.Log.Info("shark alert suppressed",
					"reason", "dust",
					"wallet", ww.ProxyWallet,
					"notional", notional,
					"min_notional", minN)
			}
			metrics.Inc("shark_alerts_suppressed_dust_total")
			return false
		}
	}

	// ── Gate passed → build and send alert ───────────────────────────────
	ev, _ := w.Store.GetLatestSharkEvidence(ctx, ww.ID)
	score := ww.Score
	conf := ww.Confidence
	if ev.Found {
		if ev.Score > 0 {
			score = ev.Score
		}
		if ev.Confidence > 0 {
			conf = ev.Confidence
		}
	}
	humanized := alerts.HumanizeReasons(ev.ReasonCodes, 4)
	if humanized == "" {
		humanized = alerts.HumanizeReasons([]string{"SHARK_HISTORICAL_EDGE"}, 1)
	}
	reasonCodes := append([]string{"SHARK_NEW_BET"}, ev.ReasonCodes...)

	// Build gate-aware feature snapshot
	snap := map[string]any{
		"wallet": ww.ProxyWallet, "direction": string(dir),
		"notional": notional, "price": price, "odds": odds, "payoff": payoff,
		"score": score, "confidence": conf,
		"closed_positions_count":    ev.ClosedPositionsCount,
		"historical_win_rate":       ev.HistoricalWinRate,
		"historical_roi":            ev.HistoricalROI,
		"avg_closed_position_stake": ev.AvgClosedStake,
		"realized_pnl":              ev.HistoricalPnL,
		"realized_pnl_known":        ev.HistoricalPnLKnown,
		"promotion_path":            ev.PromotionPath,
		"score_version":             ev.ScoreVersion,
		"alert_gate_enabled":        gateEnabled,
	}
	gateReasonText := ""
	if gateEnabled {
		profitIfWin := gate.ProfitIfWin
		snap["alert_gate_tier"] = string(gate.Tier)
		snap["alert_gate_pass"] = true
		snap["alert_gate_reason"] = gate.Reason
		snap["profit_if_win"] = profitIfWin
		snap["payoff_if_win"] = gate.PayoffIfWin
		snap["min_required_odds"] = gate.MinOdds
		snap["min_required_profit"] = gate.MinProfit
		gateReasonText = FormatProfitGateWhyLine(gate)
		if humanized != "" {
			gateReasonText = gateReasonText + " / " + humanized
		}

		if w.Log != nil {
			w.Log.Info("shark alert profit gate passed",
				"wallet", ww.ProxyWallet,
				"tier", string(gate.Tier),
				"notional", notional,
				"odds", gate.Odds,
				"profit_if_win", profitIfWin)
		}
	}

	decision := postgres.AlertDecision{
		AlertType:         alerts.TypeSharkBet,
		EntityType:        "watched_bet",
		EntityID:          watchedBetID,
		Severity:          "WARNING",
		ShouldSend:        true,
		UserAlertAllowed:  true,
		AdminAlertAllowed: true,
		ReasonCodes:       reasonCodes,
		FeatureSnapshot:   snap,
		DedupKey:          dedup,
	}

	// Compute profit_if_win for alert payload
	alertProfitIfWin := 0.0
	alertGateTierStr := ""
	if gateEnabled {
		alertProfitIfWin = gate.ProfitIfWin
		alertGateTierStr = string(gate.Tier)
	} else if payoff > 0 && notional > 0 {
		alertProfitIfWin = payoff - notional
	}

	body := alerts.FormatSharkBet(alerts.SharkBet{
		WalletShort:          ww.ProxyWallet,
		WalletFull:           ww.ProxyWallet,
		Pseudonym:            ww.Pseudonym,
		ProfileSlug:          ww.ProfileSlug,
		ScoringBasis:         ev.ScoringBasis,
		RealizedCycles:       ev.RealizedCycles,
		ProfitableExitRate:   ev.ProfitableExitRate,
		RealizedAvgROI:       ev.RealizedAvgROI,
		RealizedAvgNotional:  ev.RealizedAvgNotional,
		RealizedTotalPnL:     ev.RealizedTotalPnL,
		RealizedProfitFactor: ev.RealizedProfitFactor,
		Class:                ww.Class,
		Score:                score,
		Confidence:           conf,
		TotalTrades:          ev.TotalTrades,
		WinRate:              ev.HistoricalWinRate,
		RealizedPnL:          ev.HistoricalPnL,
		RealizedKnown:        ev.HistoricalPnLKnown,
		ClosedPositionsCount: ev.ClosedPositionsCount,
		HistoricalWinRate:    ev.HistoricalWinRate,
		HistoricalROI:        ev.HistoricalROI,
		AvgClosedStake:       ev.AvgClosedStake,
		HistoricalPnL:        ev.HistoricalPnL,
		HistoricalPnLKnown:   ev.HistoricalPnLKnown,
		PromotionPath:        ev.PromotionPath,
		MarketSlug:           marketSlug,
		MarketTitle:          marketTitle,
		EventSlug:            eventSlug,
		Direction:            alerts.Direction(dir),
		Outcome:              outcome,
		Side:                 strings.ToUpper(a.Side),
		Notional:             notional,
		Price:                price,
		Odds:                 odds,
		Payoff:               payoff,
		ProfitIfWin:          alertProfitIfWin,
		AlertGateTier:        alertGateTierStr,
		AlertGateReason:      gateReasonText,
		ReasonHumanized:      humanized,
		ReasonCodes:          reasonCodes,
	}, w.Links)
	out := w.Router.Route(ctx, decision, body, alerts.ChannelBets)
	if out.Err != nil && w.Log != nil {
		w.Log.Warn("shark alert send", "err", out.Err)
	}
	if out.Sent {
		metrics.Inc("alerts_shark_sent_total")
	}
	return true
}

// routeInsiderAlertV4 enforces v4 insider gates AT ALERT TIME on the new bet:
//   - notional >= MinNotionalUSD (default 20_000)
//   - price > 0 AND price <= 1/MinOdds (default 3 → price <= 0.3333)
//   - direction known
//   - lifetime trades <= MaxLifetimeForCapture (default 10)
//   - clean streak (no losing closed positions) — otherwise STREAK_BROKEN
//
// Returns true iff an alert was created (regardless of send outcome).
// All alert_decisions are persisted even when ShouldSend=false (audit).
func (w *WatchedWalletWorker) routeInsiderAlertV4(ctx context.Context, ww postgres.WatchedWallet, watchedBetID string, dir Direction, dirOutcome string, notional, price, odds, payoff float64, a dataapi.Activity, marketSlug, marketTitle, eventSlug, resolutionSource, rulesText, eventTitle string) bool {
	rules := EvaluateRulesRisk(MarketRulesInputs{
		Title: marketTitle, ResolutionSource: resolutionSource, RulesText: rulesText, EventTitle: eventTitle,
	})

	minNotional := w.InsiderParams.MinNotionalUSD
	if minNotional <= 0 {
		minNotional = 20_000
	}
	minOdds := w.InsiderParams.MinOdds
	if minOdds <= 0 {
		minOdds = 3.0
	}
	maxLifetime := w.InsiderParams.MaxLifetimeForCapture
	if maxLifetime <= 0 {
		maxLifetime = 10
	}

	// Fetch fresh historical stats to decide streak state at alert time.
	stats, _ := w.Store.GetHistoricalCloseStats(ctx, ww.ID)
	cleanStreak := stats.LosingCount == 0

	// Lifetime trade count via cheap probe; if our backfill has filled the
	// trades counter we use it, otherwise probe a single page.
	lifetimeTrades := 0
	if bf, ok, _ := w.Store.GetBackfillRecord(ctx, ww.ID); ok && bf.TradesFetched > 0 {
		lifetimeTrades = bf.TradesFetched
	} else {
		trades, _, err := w.DataAPI.GetTrades(ctx, ww.ProxyWallet, "", false, maxLifetime+5)
		if err == nil {
			lifetimeTrades = len(trades)
		}
	}

	highOdds := price > 0 && price <= 1.0/minOdds
	bigBet := notional >= minNotional
	directionKnown := dir != ""

	streakState := "clean"
	if !cleanStreak {
		streakState = "broken"
	} else if stats.ProfitableCount > 0 {
		streakState = "continues"
	}

	gatesPass := directionKnown && bigBet && highOdds && lifetimeTrades >= 1 && lifetimeTrades <= maxLifetime && cleanStreak
	alertType := alerts.TypeInsiderFirstBigBet
	if streakState == "continues" {
		alertType = alerts.TypeInsiderStreakContinues
	}
	if !cleanStreak {
		alertType = alerts.TypeInsiderStreakBroken
	}

	shouldSend := gatesPass && rules.Level != RulesRiskBlocking
	userAllowed := shouldSend
	channel := alerts.ChannelBets
	if !shouldSend {
		channel = alerts.ChannelAdmin
	}

	reasonCodes := []string{"INSIDER_LIKE_NEW_BET", "NOT_LEGAL_INSIDER_CLAIM"}
	if bigBet {
		reasonCodes = append(reasonCodes, "FIRST_LARGE_BET_20K", "LARGE_ANONYMOUS_CONVICTION")
	}
	if highOdds {
		reasonCodes = append(reasonCodes, "HIGH_ODDS_3X")
	}
	if cleanStreak {
		reasonCodes = append(reasonCodes, "NO_LOSSES_YET")
		if stats.ProfitableCount > 0 {
			reasonCodes = append(reasonCodes, "WINNING_STREAK")
		}
	} else {
		reasonCodes = append(reasonCodes, "STREAK_BROKEN")
	}
	if rules.Level == RulesRiskHigh {
		reasonCodes = append(reasonCodes, "RULES_RISK_HIGH")
	}
	if !bigBet {
		reasonCodes = append(reasonCodes, "BELOW_NOTIONAL_THRESHOLD")
	}
	if !highOdds {
		reasonCodes = append(reasonCodes, "ODDS_BELOW_3X")
	}
	if lifetimeTrades > maxLifetime {
		reasonCodes = append(reasonCodes, "MATURE_WALLET_NOT_INSIDER_LIKE")
	}
	if !directionKnown {
		reasonCodes = append(reasonCodes, "MISSING_TRADE_DIRECTION")
	}

	dedup := alerts.DedupKey(alertType, a.TransactionHash, ww.ProxyWallet, string(dir), a.ConditionID)
	decision := postgres.AlertDecision{
		AlertType:         alertType,
		EntityType:        "watched_bet",
		EntityID:          watchedBetID,
		Severity:          "HIGH",
		ShouldSend:        shouldSend,
		UserAlertAllowed:  userAllowed,
		AdminAlertAllowed: true,
		ReasonCodes:       reasonCodes,
		FeatureSnapshot: map[string]any{
			"wallet":           ww.ProxyWallet,
			"direction":        string(dir),
			"notional":         notional,
			"price":            price,
			"odds":             odds,
			"payoff":           payoff,
			"rules_risk_level": string(rules.Level),
			"lifetime_trades":  lifetimeTrades,
			"streak_state":     streakState,
			"closed_wins":      stats.ProfitableCount,
			"closed_losses":    stats.LosingCount,
			"score_version":    ScoreVersion,
			"gates_pass":       gatesPass,
		},
		DedupKey: dedup,
	}

	humanized := "insider-like " + streakLabelLocal(streakState)
	if rules.Level == RulesRiskHigh {
		humanized += " · " + string(rules.Level)
	}
	firstTradeAtStr := ""
	if stats.LastObservedAt != nil {
		firstTradeAtStr = stats.LastObservedAt.UTC().Format("2006-01-02")
	}
	insiderProfitIfWin := 0.0
	if payoff > 0 && notional > 0 {
		insiderProfitIfWin = payoff - notional
	}
	body := alerts.FormatInsiderBet(alerts.InsiderBet{
		WalletShort:      ww.ProxyWallet,
		WalletFull:       ww.ProxyWallet,
		ProfileSlug:      ww.ProfileSlug,
		LifetimeTrades:   lifetimeTrades,
		LifetimeMarkets:  0,
		StreakState:      streakState,
		ClosedWins:       stats.ProfitableCount,
		ClosedLosses:     stats.LosingCount,
		FirstTradeAt:     firstTradeAtStr,
		MarketSlug:       marketSlug,
		MarketTitle:      marketTitle,
		EventSlug:        eventSlug,
		Direction:        alerts.Direction(dir),
		DirectionOutcome: dirOutcome,
		Notional:         notional,
		Price:            price,
		Odds:             odds,
		Payoff:           payoff,
		ProfitIfWin:      insiderProfitIfWin,
		ReasonHumanized:  humanized,
		ReasonCodes:      reasonCodes,
		Severity:         "HIGH",
	}, w.Links)
	out := w.Router.Route(ctx, decision, body, channel)
	if out.Err != nil && w.Log != nil {
		w.Log.Warn("insider alert send", "err", out.Err)
	}
	if out.Sent {
		metrics.Inc("alerts_insider_sent_total")
	}
	return true
}

func streakLabelLocal(s string) string {
	switch s {
	case "continues":
		return "clean streak continues"
	case "broken":
		return "streak just broken"
	case "clean":
		return "first big bet (clean history)"
	default:
		return "candidate"
	}
}
