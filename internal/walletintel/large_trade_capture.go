package walletintel

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// LargeTradeCaptureWorker scans /trades?market=<conditionId> for hotset
// markets and catches unknown wallets placing large, high-odds bets — the
// event-driven insider-like capture path described by NON-NEGOTIABLE:
// "Unknown wallet with large first/early trade must be capturable without
//
//	prior watchlist."
//
// The worker is intentionally bounded:
//   - Only hotset markets (top by volume+liquidity) — bounded by Store.ListHotsetCandidates.
//   - Per-market trade page is small (limit=100, taker-only by default).
//   - Per cycle, we look back at most LookbackTrades records per market.
//   - Wallet's lifetime is sourced via cheap /trades?user=…&limit=12 probe.
//   - Dedup by (transaction_hash, alert_type) via alert_decisions.dedup_key.
//
// On a qualifying trade we:
//  1. Upsert the wallet (track is_new).
//  2. Insert/update wallet_score insider_like_score with explicit reasons.
//  3. Upsert wallet_watchlist as insider_like active.
//  4. Persist alert_decision INSIDER_LIKE_FIRST_BIG_BET (admin-side dedup
//     lives on the watched_wallet path; this is the *first-touch* path).
//  5. Route through AlertRouter to the bets channel.
type LargeTradeCaptureWorker struct {
	DataAPI       *dataapi.Client
	Store         *postgres.Store
	Router        *alerts.Router
	Log           *slog.Logger
	Links         alerts.LinkBuilder
	InsiderParams InsiderParams

	Interval        time.Duration
	HotsetSize      int
	TradesPerMarket int
	LookbackTrades  int

	// Set explicitly to point at the runner's category lookup.
	CategoryFor func(ctx context.Context, conditionID string) string

	// Optional override for the in-process insider streak/discovery emitter.
	// nil-safe — set in app wiring.
	Discovery *DiscoveryEmitter

	// guard against duplicate processing inside the same process run
	seen sync.Map // key: transactionHash → struct{}
}

func (w *LargeTradeCaptureWorker) defaults() {
	if w.Interval <= 0 {
		w.Interval = 90 * time.Second
	}
	if w.HotsetSize <= 0 {
		w.HotsetSize = 50
	}
	if w.TradesPerMarket <= 0 {
		w.TradesPerMarket = 100
	}
	if w.LookbackTrades <= 0 {
		w.LookbackTrades = 100
	}
}

func (w *LargeTradeCaptureWorker) Run(ctx context.Context) error {
	w.defaults()
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

func (w *LargeTradeCaptureWorker) runOnce(ctx context.Context) {
	start := time.Now()
	markets, err := w.Store.ListHotsetCandidates(ctx, w.HotsetSize)
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("largetrade hotset list", "err", err)
		}
		return
	}
	scanned, qualified, captured := 0, 0, 0
	minNotional := w.InsiderParams.MinNotionalUSD
	if minNotional <= 0 {
		minNotional = 20_000
	}
	minOdds := w.InsiderParams.MinOdds
	if minOdds <= 0 {
		minOdds = 3.0
	}
	maxPrice := 1.0 / minOdds

	for _, m := range markets {
		select {
		case <-ctx.Done():
			return
		default:
		}
		trades, _, err := w.DataAPI.GetTradesPaginated(ctx, "", m.ConditionID, false, w.TradesPerMarket, 0)
		if err != nil {
			metrics.Inc("largetrade_trades_errors_total")
			continue
		}
		scanned += len(trades)
		for _, t := range trades {
			usd := t.UsdcSize.Float64()
			if usd == 0 {
				usd = t.Size.Float64() * t.Price.Float64()
			}
			price := t.Price.Float64()
			if usd < minNotional || price <= 0 || price > maxPrice {
				continue
			}
			if strings.ToUpper(t.Side) != "BUY" {
				continue
			}
			if _, dup := w.seen.LoadOrStore(t.TransactionHash, struct{}{}); dup {
				continue
			}
			qualified++
			if w.captureWallet(ctx, m, t, usd, price) {
				captured++
			}
		}
	}
	if w.Log != nil {
		w.Log.Info("largetrade capture cycle",
			"markets", len(markets),
			"trades_scanned", scanned,
			"qualifying_trades", qualified,
			"captured", captured,
			"duration", time.Since(start).String())
	}
}

// captureWallet upserts wallet, fetches cheap lifetime probe, validates
// insider-like gates at event time, persists scoring artifacts, and routes
// the alert. Returns true iff alert_decision was created.
func (w *LargeTradeCaptureWorker) captureWallet(ctx context.Context, m postgres.MarketSummary, t dataapi.Trade, notional, price float64) bool {
	walletID, isNew, err := w.Store.UpsertWalletReturn(ctx, postgres.Wallet{
		ProxyWallet: t.ProxyWallet,
		Pseudonym:   t.Pseudonym,
	})
	if err != nil {
		return false
	}
	dirRes, dirErr := DirectionOf(t.Outcome, t.Side)
	if dirErr != nil {
		return false
	}
	dir := dirRes.Direction
	dirOutcome := dirRes.DirectionOutcome

	// Cheap lifetime probe.
	maxLifetime := w.InsiderParams.MaxLifetimeForCapture
	if maxLifetime <= 0 {
		maxLifetime = 10
	}
	probe, _, _ := w.DataAPI.GetTrades(ctx, t.ProxyWallet, "", false, maxLifetime+5)
	lifetimeTrades := len(probe)
	if lifetimeTrades > maxLifetime {
		// mature wallet — outside event-driven insider capture window
		return false
	}

	// Streak state from historical closed positions (best-effort).
	stats, _ := w.Store.GetHistoricalCloseStats(ctx, walletID)
	cleanStreak := stats.LosingCount == 0
	streakState := "clean"
	if stats.ProfitableCount > 0 && cleanStreak {
		streakState = "continues"
	} else if !cleanStreak {
		streakState = "broken"
	}

	cat := ""
	if w.CategoryFor != nil {
		cat = w.CategoryFor(ctx, t.ConditionID)
	}
	highImpact := categoryMatches(cat, w.InsiderParams.HighImpactCategories)

	// Build insider WalletFacts + NewBetContext synthetically and score.
	facts := WalletFacts{
		WalletID:                walletID,
		Wallet:                  t.ProxyWallet,
		LifetimeTradeCount:      lifetimeTrades,
		LifetimeProfitableCount: stats.ProfitableCount,
		LifetimeLosingCount:     stats.LosingCount,
		InsiderStreakClean:      cleanStreak,
		HistoricalPnLKnown:      stats.ClosedCount > 0,
		NewBet: &NewBetContext{
			Direction:          dir,
			Notional:           notional,
			Price:              price,
			Outcome:            t.Outcome,
			MarketSlug:         m.Slug,
			MarketCategory:     cat,
			MarketIsHighImpact: highImpact,
		},
		Now: time.Now(),
	}
	insider := ScoreInsiderLike(facts, w.InsiderParams)
	// Persist scoring even when not promoted — the audit trail explains
	// why the candidate was rejected (insider-namespace reasons only).
	_, _ = w.Store.InsertScore(ctx, walletID, toScoreRow(insider))
	metrics.Inc("wallet_scores_total{strategy=insider,result=" + classifyResult(insider) + "}")
	if !insider.Promote {
		return false
	}

	// Promote to watchlist (active or watch_only depending on streak).
	status := StatusActive
	if !cleanStreak {
		status = StatusStreakBroken
	}
	if err := w.Store.UpsertWatchlist(ctx, postgres.WatchlistRow{
		WalletID:        walletID,
		Class:           "insider_like",
		Status:          status,
		Score:           insider.Score,
		Confidence:      insider.Confidence,
		ReasonCodes:     insider.ReasonCodes,
		FeatureSnapshot: map[string]any(insider.FeatureSnapshot),
		ScoreVersion:    ScoreVersion,
	}); err != nil {
		if w.Log != nil {
			w.Log.Warn("largetrade upsert watchlist", "err", err)
		}
	}

	// Bet alert via AlertRouter (bets channel).
	alertType := alerts.TypeInsiderFirstBigBet
	if streakState == "continues" {
		alertType = alerts.TypeInsiderStreakContinues
	}
	dedup := alerts.DedupKey(alertType, t.TransactionHash, t.ProxyWallet, string(dir), t.ConditionID)
	betSnap, _ := json.Marshal(insider.FeatureSnapshot)
	var snapMap map[string]any
	_ = json.Unmarshal(betSnap, &snapMap)
	if snapMap == nil {
		snapMap = map[string]any{}
	}
	snapMap["captured_via"] = "large_trade_scan"
	snapMap["transaction_hash"] = t.TransactionHash
	snapMap["wallet_was_new"] = isNew
	odds := 0.0
	if price > 0 {
		odds = 1.0 / price
	}
	decision := postgres.AlertDecision{
		AlertType:         alertType,
		EntityType:        "wallet_trade",
		EntityID:          walletID,
		Severity:          "HIGH",
		ShouldSend:        true,
		UserAlertAllowed:  true,
		AdminAlertAllowed: true,
		ReasonCodes:       insider.ReasonCodes,
		FeatureSnapshot:   snapMap,
		DedupKey:          dedup,
	}
	body := alerts.FormatInsiderBet(alerts.InsiderBet{
		WalletShort:      t.ProxyWallet,
		WalletFull:       t.ProxyWallet,
		LifetimeTrades:   lifetimeTrades,
		LifetimeMarkets:  0,
		StreakState:      streakState,
		ClosedWins:       stats.ProfitableCount,
		ClosedLosses:     stats.LosingCount,
		MarketSlug:       m.Slug,
		MarketTitle:      m.Question,
		EventSlug:        m.EventID,
		Direction:        alerts.Direction(dir),
		DirectionOutcome: dirOutcome,
		Notional:         notional,
		Price:            price,
		Odds:             odds,
		Payoff:           notional * odds,
		ReasonHumanized:  alerts.HumanizeReasons(insider.ReasonCodes, 4),
		ReasonCodes:      insider.ReasonCodes,
		Severity:         "HIGH",
	}, w.Links)
	out := w.Router.Route(ctx, decision, body, alerts.ChannelBets)
	if out.Err != nil && w.Log != nil {
		w.Log.Warn("largetrade alert send", "err", out.Err)
	}
	if out.Sent {
		metrics.Inc("alerts_insider_sent_total")
		metrics.Inc("largetrade_capture_total{kind=" + alertType + "}")
	}

	// Admin discovery alert (separate channel, separate dedup).
	if w.Discovery != nil {
		promo := WatchlistPromotion{
			WalletID:        walletID,
			Class:           "insider_like",
			Status:          status,
			Score:           insider.Score,
			Confidence:      insider.Confidence,
			ReasonCodes:     insider.ReasonCodes,
			FeatureSnapshot: insider.FeatureSnapshot,
			ScoreVersion:    ScoreVersion,
		}
		_ = w.Discovery.EmitOnTransition(ctx, walletID, t.ProxyWallet, nil, promo, ScoreResult{}, insider)
	}

	if w.Log != nil {
		w.Log.Info("largetrade insider captured",
			"wallet", t.ProxyWallet,
			"market_slug", m.Slug,
			"notional", notional,
			"price", price,
			"lifetime_trades", lifetimeTrades,
			"streak_state", streakState,
			"is_new_wallet", isNew,
			"alert_type", alertType)
	}
	return true
}
