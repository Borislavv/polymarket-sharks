package walletintel

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// LuckySpikeWorker scans all active markets, gathers high-activity wallet
// candidates, computes weekly realized-cycle metrics, and runs
// lucky_spike_score.
//
// This worker is intentionally admin-first: it stores strategy score rows for
// audit and emits admin alerts when a wallet passes the suspicious weekly
// pattern gates.
type LuckySpikeWorker struct {
	DataAPI *dataapi.Client
	Store   *postgres.Store
	Router  *alerts.Router
	Log     *slog.Logger
	Links   alerts.LinkBuilder

	Interval                 time.Duration
	MaxMarkets               int // 0 = all active markets
	MarketTradesLimit        int
	MarketConcurrency        int
	CandidateTradePageSize   int
	CandidateTradeMaxPages   int
	CandidateMinSampleTrades int
	MaxCandidateWallets      int
	WalletConcurrency        int
	WalletTradePageSize      int
	WalletTradeMaxPages      int
	WalletActivityMaxPages   int
	PerWalletFetchTimout     time.Duration

	Params LuckySpikeParams
}

const (
	luckySpikeActivityOffsetCap      = 3000
	luckySpikeDefaultActivityMaxPage = 90
)

type luckyCandidate struct {
	Wallet             string
	Pseudonym          string
	SampleTradeCount   int
	SampleFirstTradeAt time.Time
	SampleLastTradeAt  time.Time
}

func (w *LuckySpikeWorker) defaults() {
	if w.Interval <= 0 {
		w.Interval = 30 * time.Minute
	}
	if w.MarketTradesLimit <= 0 {
		w.MarketTradesLimit = 40
	}
	if w.MarketConcurrency <= 0 {
		w.MarketConcurrency = 8
	}
	if w.CandidateTradePageSize <= 0 {
		w.CandidateTradePageSize = 500
	}
	if w.CandidateTradeMaxPages <= 0 {
		w.CandidateTradeMaxPages = 120
	}
	if w.CandidateMinSampleTrades <= 0 {
		w.CandidateMinSampleTrades = 6
	}
	if w.MaxCandidateWallets <= 0 {
		w.MaxCandidateWallets = 2000
	}
	if w.WalletConcurrency <= 0 {
		w.WalletConcurrency = 6
	}
	if w.WalletTradePageSize <= 0 {
		w.WalletTradePageSize = 500
	}
	if w.WalletTradeMaxPages <= 0 {
		w.WalletTradeMaxPages = 10
	}
	if w.WalletActivityMaxPages <= 0 {
		w.WalletActivityMaxPages = luckySpikeDefaultActivityMaxPage
	}
	if w.PerWalletFetchTimout <= 0 {
		w.PerWalletFetchTimout = 120 * time.Second
	}
}

func (w *LuckySpikeWorker) lookback() time.Duration {
	if w.Params.Lookback > 0 {
		return w.Params.Lookback
	}
	return 7 * 24 * time.Hour
}

func (w *LuckySpikeWorker) minTradesPerWeek() int {
	if w.Params.MinTradesPerWeek > 0 {
		return w.Params.MinTradesPerWeek
	}
	lb := w.lookback()
	interval := w.Params.MaxAvgTradeInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return int(lb / interval)
}

func (w *LuckySpikeWorker) minTradesPerMonth() int {
	if w.Params.MinTradesPerMonth > 0 {
		return w.Params.MinTradesPerMonth
	}
	interval := w.Params.MaxAvgTradeInterval
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	return int((30 * 24 * time.Hour) / interval)
}

func (w *LuckySpikeWorker) Run(ctx context.Context) error {
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

func (w *LuckySpikeWorker) runOnce(ctx context.Context) {
	if w.Store == nil || w.DataAPI == nil {
		return
	}
	start := time.Now()
	weekLookback := w.lookback()
	monthLookback := 30 * 24 * time.Hour
	weekStart := start.Add(-weekLookback)
	monthStart := start.Add(-monthLookback)

	var (
		evaluated        int64
		promoted         int64
		alerted          int64
		errorsN          int64
		candidatesQueued int64
	)
	candidateCh := make(chan luckyCandidate, w.WalletConcurrency*2)
	var wg sync.WaitGroup
	for i := 0; i < w.WalletConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range candidateCh {
				atomic.AddInt64(&evaluated, 1)
				promote, sent, err := w.evaluateCandidate(ctx, c, weekStart, monthStart, weekLookback, monthLookback)
				if err != nil {
					atomic.AddInt64(&errorsN, 1)
					metrics.Inc("lucky_spike_worker_errors_total{stage=evaluate_wallet}")
					if w.Log != nil {
						w.Log.Warn("lucky spike: evaluate wallet", "wallet", c.Wallet, "err", err)
					}
					continue
				}
				if promote {
					atomic.AddInt64(&promoted, 1)
				}
				if sent {
					atomic.AddInt64(&alerted, 1)
				}
			}
		}()
	}

	scannedTrades, discovered, scanErrors := w.streamCandidates(ctx, weekStart, candidateCh)
	atomic.StoreInt64(&candidatesQueued, discovered)
	close(candidateCh)
	wg.Wait()

	metrics.SetGauge("lucky_spike_candidates_last_cycle", candidatesQueued)
	metrics.Add("lucky_spike_trades_sampled_total", scannedTrades)
	metrics.Add("lucky_spike_wallets_evaluated_total", evaluated)
	metrics.Add("lucky_spike_wallets_promoted_total", promoted)
	metrics.Add("lucky_spike_alerts_sent_total", alerted)
	if errorsN > 0 {
		metrics.Add("lucky_spike_wallet_errors_total", errorsN)
	}
	if scanErrors > 0 {
		metrics.Add("lucky_spike_scan_errors_total", scanErrors)
	}
	if w.Log != nil {
		w.Log.Info("lucky spike cycle completed",
			"candidate_source", "global_trades_stream",
			"candidate_pages", w.CandidateTradeMaxPages,
			"candidates", candidatesQueued,
			"trades_sampled", scannedTrades,
			"wallets_evaluated", evaluated,
			"promoted", promoted,
			"alerts_sent", alerted,
			"errors", errorsN,
			"duration", time.Since(start).String())
	}
}

func (w *LuckySpikeWorker) streamCandidates(ctx context.Context, weekStart time.Time, out chan<- luckyCandidate) (int64, int64, int64) {
	stats := make(map[string]luckyCandidate, w.MaxCandidateWallets*2)
	enqueued := make(map[string]struct{}, w.MaxCandidateWallets)
	var (
		scanned int64
		errorsN int64
	)

	for page := 0; page < w.CandidateTradeMaxPages; page++ {
		select {
		case <-ctx.Done():
			return scanned, int64(len(enqueued)), errorsN
		default:
		}

		trades, _, err := w.DataAPI.GetTradesPaginated(ctx, "", "", false, w.CandidateTradePageSize, page*w.CandidateTradePageSize)
		if err != nil {
			errorsN++
			metrics.Inc("lucky_spike_worker_errors_total{stage=global_trades}")
			if w.Log != nil {
				w.Log.Warn("lucky spike: global trades page", "page", page, "err", err)
			}
			continue
		}
		if len(trades) == 0 {
			break
		}
		atomic.AddInt64(&scanned, int64(len(trades)))

		oldestTs := int64((1 << 63) - 1)
		for _, t := range trades {
			ts := time.Unix(t.Timestamp.Int64(), 0).UTC()
			if t.Timestamp.Int64() < oldestTs {
				oldestTs = t.Timestamp.Int64()
			}
			if ts.Before(weekStart) {
				continue
			}
			wallet := strings.ToLower(strings.TrimSpace(t.ProxyWallet))
			if wallet == "" {
				continue
			}
			c := stats[wallet]
			if c.Wallet == "" {
				c.Wallet = wallet
			}
			if c.Pseudonym == "" && strings.TrimSpace(t.Pseudonym) != "" {
				c.Pseudonym = strings.TrimSpace(t.Pseudonym)
			}
			c.SampleTradeCount++
			if c.SampleFirstTradeAt.IsZero() || ts.Before(c.SampleFirstTradeAt) {
				c.SampleFirstTradeAt = ts
			}
			if c.SampleLastTradeAt.IsZero() || ts.After(c.SampleLastTradeAt) {
				c.SampleLastTradeAt = ts
			}
			stats[wallet] = c

			if len(enqueued) >= w.MaxCandidateWallets {
				continue
			}
			if _, ok := enqueued[wallet]; ok || !w.shouldQueueCandidate(c) {
				continue
			}
			enqueued[wallet] = struct{}{}
			select {
			case <-ctx.Done():
				return scanned, int64(len(enqueued)), errorsN
			case out <- c:
				metrics.Inc("lucky_spike_candidates_streamed_total")
			}
		}

		if len(enqueued) >= w.MaxCandidateWallets {
			metrics.Inc("lucky_spike_candidates_truncated_total")
			break
		}
		if oldestTs <= weekStart.Unix() || len(trades) < w.CandidateTradePageSize {
			break
		}
	}

	return scanned, int64(len(enqueued)), errorsN
}

func (w *LuckySpikeWorker) shouldQueueCandidate(c luckyCandidate) bool {
	if c.SampleTradeCount < w.CandidateMinSampleTrades {
		return false
	}
	avg := candidateSampleAvgInterval(c)
	if avg <= 0 {
		return false
	}
	maxInterval := w.Params.MaxAvgTradeInterval
	if maxInterval <= 0 {
		maxInterval = 2 * time.Minute
	}
	return avg <= maxInterval
}

func candidateSampleAvgInterval(c luckyCandidate) time.Duration {
	if c.SampleTradeCount <= 1 {
		return 0
	}
	coverage := c.SampleLastTradeAt.Sub(c.SampleFirstTradeAt)
	if coverage <= 0 {
		return time.Second
	}
	return coverage / time.Duration(c.SampleTradeCount-1)
}

func avgIntervalFromCoverage(count int, coverage, fallbackWindow time.Duration) time.Duration {
	if count <= 0 {
		return 0
	}
	if count == 1 {
		return fallbackWindow
	}
	if coverage <= 0 {
		return time.Second
	}
	return coverage / time.Duration(count-1)
}

type positionProfitAggregate struct {
	Count         int
	CashPnL       float64
	EntryNotional float64
}

func (w *LuckySpikeWorker) enrichProfitFromPositions(ctx context.Context, wallet string, weeklyMarkets, monthlyMarkets map[string]struct{}, facts *LuckySpikeFacts) {
	if w.DataAPI == nil || facts == nil {
		return
	}
	positions, err := w.fetchWalletPositions(ctx, wallet)
	if err != nil {
		metrics.Inc("lucky_spike_worker_errors_total{stage=positions_profit}")
		return
	}
	weeklyProfilePnL, weeklyProfileOK := w.fetchProfilePnLDelta(ctx, wallet, "1w")
	monthlyProfilePnL, monthlyProfileOK := w.fetchProfilePnLDelta(ctx, wallet, "1m")

	weekAgg := aggregatePositionProfit(positions, weeklyMarkets)
	facts.WeeklyProfitPositions = weekAgg.Count
	if weekAgg.EntryNotional > 0 {
		facts.WeeklyRealizedPnL = weekAgg.CashPnL
		if weeklyProfileOK {
			facts.WeeklyRealizedPnL = weeklyProfilePnL
			facts.WeeklyProfitSource = "profile_pnl_delta"
		} else {
			facts.WeeklyProfitSource = "positions_cash_pnl"
		}
		facts.WeeklyEntryNotional = weekAgg.EntryNotional
		facts.WeeklyProfitPct = facts.WeeklyRealizedPnL / weekAgg.EntryNotional
		facts.WeeklyProfitPctKnown = true
	}
	monthAgg := aggregatePositionProfit(positions, monthlyMarkets)
	facts.MonthlyProfitPositions = monthAgg.Count
	if monthAgg.EntryNotional > 0 {
		facts.MonthlyRealizedPnL = monthAgg.CashPnL
		if monthlyProfileOK {
			facts.MonthlyRealizedPnL = monthlyProfilePnL
			facts.MonthlyProfitSource = "profile_pnl_delta"
		} else {
			facts.MonthlyProfitSource = "positions_cash_pnl"
		}
		facts.MonthlyEntryNotional = monthAgg.EntryNotional
		facts.MonthlyProfitPct = facts.MonthlyRealizedPnL / monthAgg.EntryNotional
		facts.MonthlyProfitPctKnown = true
	}
}

func (w *LuckySpikeWorker) fetchProfilePnLDelta(ctx context.Context, wallet, interval string) (float64, bool) {
	series, _, err := w.DataAPI.GetUserPnLSeries(ctx, wallet, interval, "1h")
	if err != nil || len(series) < 2 {
		if err != nil {
			metrics.Inc("lucky_spike_worker_errors_total{stage=profile_pnl}")
		}
		return 0, false
	}
	first := series[0].P.Float64()
	last := series[len(series)-1].P.Float64()
	return last - first, true
}

func (w *LuckySpikeWorker) fetchWalletPositions(ctx context.Context, wallet string) ([]dataapi.ClosedPosition, error) {
	var out []dataapi.ClosedPosition
	pageSize := w.WalletTradePageSize
	if pageSize <= 0 {
		pageSize = 500
	}
	maxPages := w.WalletTradeMaxPages
	if maxPages <= 0 {
		maxPages = 10
	}
	for page := 0; page < maxPages; page++ {
		res, err := w.DataAPI.GetClosedPositionsPaginated(ctx, wallet, pageSize, page*pageSize)
		if err != nil {
			return out, err
		}
		out = append(out, res.Items...)
		if len(res.Items) < pageSize || !res.HasMore {
			break
		}
	}
	return out, nil
}

func aggregatePositionProfit(positions []dataapi.ClosedPosition, markets map[string]struct{}) positionProfitAggregate {
	if len(markets) == 0 {
		return positionProfitAggregate{}
	}
	var out positionProfitAggregate
	for _, p := range positions {
		if _, ok := markets[p.ConditionID]; !ok {
			continue
		}
		entry := positionEntryNotional(p)
		if entry <= 0 {
			continue
		}
		out.Count++
		out.EntryNotional += entry
		out.CashPnL += p.CashPnL
	}
	return out
}

func positionEntryNotional(p dataapi.ClosedPosition) float64 {
	if p.InitialValue > 0 {
		return p.InitialValue
	}
	totalBought := p.TotalBought
	avgPrice := p.AvgPrice
	if totalBought > 0 && avgPrice > 0 {
		return totalBought * avgPrice
	}
	current := p.CurrentValue
	cashPnL := p.CashPnL
	if inferred := current - cashPnL; inferred > 0 {
		return inferred
	}
	return 0
}

func (w *LuckySpikeWorker) collectCandidates(ctx context.Context, markets []postgres.MarketSummary, weekStart time.Time) ([]luckyCandidate, int64) {
	sem := make(chan struct{}, w.MarketConcurrency)
	var wg sync.WaitGroup
	var (
		mu          sync.Mutex
		out         = make(map[string]luckyCandidate, w.MaxCandidateWallets)
		scanned     int64
		maxCandsHit int64
	)

	for _, m := range markets {
		select {
		case <-ctx.Done():
			return mapToSlice(out), scanned
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(m postgres.MarketSummary) {
			defer wg.Done()
			defer func() { <-sem }()
			trades, _, err := w.DataAPI.GetTradesPaginated(ctx, "", m.ConditionID, false, w.MarketTradesLimit, 0)
			if err != nil {
				metrics.Inc("lucky_spike_worker_errors_total{stage=market_trades}")
				return
			}
			atomic.AddInt64(&scanned, int64(len(trades)))
			for _, t := range trades {
				ts := time.Unix(t.Timestamp.Int64(), 0).UTC()
				if ts.Before(weekStart) {
					continue
				}
				wallet := strings.ToLower(strings.TrimSpace(t.ProxyWallet))
				if wallet == "" {
					continue
				}
				mu.Lock()
				if len(out) >= w.MaxCandidateWallets {
					mu.Unlock()
					atomic.StoreInt64(&maxCandsHit, 1)
					continue
				}
				c, ok := out[wallet]
				if !ok {
					out[wallet] = luckyCandidate{
						Wallet:    wallet,
						Pseudonym: strings.TrimSpace(t.Pseudonym),
					}
					mu.Unlock()
					continue
				}
				if c.Pseudonym == "" && strings.TrimSpace(t.Pseudonym) != "" {
					c.Pseudonym = strings.TrimSpace(t.Pseudonym)
					out[wallet] = c
				}
				mu.Unlock()
			}
		}(m)
	}
	wg.Wait()

	if maxCandsHit == 1 {
		metrics.Inc("lucky_spike_candidates_truncated_total")
	}
	return mapToSlice(out), scanned
}

func (w *LuckySpikeWorker) evaluateCandidate(parent context.Context, c luckyCandidate, weekStart, monthStart time.Time, weekLookback, monthLookback time.Duration) (bool, bool, error) {
	ctx, cancel := context.WithTimeout(parent, w.PerWalletFetchTimout)
	defer cancel()

	walletID, _, err := w.Store.UpsertWalletReturn(ctx, postgres.Wallet{
		ProxyWallet: c.Wallet,
		Pseudonym:   c.Pseudonym,
	})
	if err != nil {
		return false, false, err
	}

	trades, partialReason, err := w.fetchWalletTrades(ctx, c.Wallet, weekStart)
	if err != nil {
		return false, false, err
	}
	if len(trades) == 0 {
		return false, false, nil
	}
	sort.Slice(trades, func(i, j int) bool {
		return trades[i].Timestamp.Int64() < trades[j].Timestamp.Int64()
	})

	weeklyTrades := make([]dataapi.Trade, 0, len(trades))
	monthlyTrades := make([]dataapi.Trade, 0, len(trades))
	for _, t := range trades {
		ts := time.Unix(t.Timestamp.Int64(), 0).UTC()
		if ts.Before(monthStart) {
			continue
		}
		monthlyTrades = append(monthlyTrades, t)
		if ts.Before(weekStart) {
			continue
		}
		weeklyTrades = append(weeklyTrades, t)
	}
	if len(weeklyTrades) == 0 && len(monthlyTrades) == 0 {
		return false, false, nil
	}

	coverage := time.Duration(0)
	var firstWeekTs, lastWeekTs time.Time
	if len(weeklyTrades) > 0 {
		firstWeekTs = time.Unix(weeklyTrades[0].Timestamp.Int64(), 0).UTC()
		lastWeekTs = time.Unix(weeklyTrades[len(weeklyTrades)-1].Timestamp.Int64(), 0).UTC()
		if lastWeekTs.After(firstWeekTs) {
			coverage = lastWeekTs.Sub(firstWeekTs)
		}
	}
	monthlyCoverage := time.Duration(0)
	if len(monthlyTrades) > 0 {
		firstMonthTs := time.Unix(monthlyTrades[0].Timestamp.Int64(), 0).UTC()
		lastMonthTs := time.Unix(monthlyTrades[len(monthlyTrades)-1].Timestamp.Int64(), 0).UTC()
		if lastMonthTs.After(firstMonthTs) {
			monthlyCoverage = lastMonthTs.Sub(firstMonthTs)
		}
	}

	distinct := map[string]struct{}{}
	monthlyDistinct := map[string]struct{}{}
	for _, t := range monthlyTrades {
		if t.ConditionID != "" {
			monthlyDistinct[t.ConditionID] = struct{}{}
		}
	}
	sumNotional := 0.0
	for _, t := range weeklyTrades {
		if t.ConditionID != "" {
			distinct[t.ConditionID] = struct{}{}
		}
		n := t.UsdcSize.Float64()
		if n == 0 {
			n = t.Size.Float64() * t.Price.Float64()
		}
		sumNotional += n
	}
	avgNotional := 0.0
	if len(weeklyTrades) > 0 {
		avgNotional = sumNotional / float64(len(weeklyTrades))
	}

	replay := tradesToReplay(trades)
	cycles := ReconstructRealizedTrades(walletID, replay)
	weekCycles := 0
	weekWins := 0
	weekLosses := 0
	monthCycles := 0
	for _, c := range cycles {
		if c.ExitTime == nil {
			continue
		}
		if !c.ExitTime.Before(monthStart) {
			monthCycles++
		}
		if c.ExitTime.Before(weekStart) {
			continue
		}
		weekCycles++
		if c.RealizedPnL > 0 {
			weekWins++
		} else if c.RealizedPnL < 0 {
			weekLosses++
		}
	}

	lastTradeAt := lastWeekTs
	if lastTradeAt.IsZero() && len(monthlyTrades) > 0 {
		lastTradeAt = time.Unix(monthlyTrades[len(monthlyTrades)-1].Timestamp.Int64(), 0).UTC()
	}

	facts := LuckySpikeFacts{
		WeeklyTradeCount:        len(weeklyTrades),
		WeeklyDistinctMarkets:   len(distinct),
		WeeklyRealizedCycles:    weekCycles,
		WeeklyProfitableCycles:  weekWins,
		WeeklyLosingCycles:      weekLosses,
		WeeklyCoverage:          coverage,
		WeeklyAvgTradeNotional:  avgNotional,
		WeeklyProfitSource:      "missing",
		MonthlyTradeCount:       len(monthlyTrades),
		MonthlyRealizedCycles:   monthCycles,
		MonthlyCoverage:         monthlyCoverage,
		MonthlyProfitSource:     "missing",
		LastTradeAt:             lastTradeAt,
		DataQuality:             dataQualityFromPartial(partialReason),
		TradeHistoryPartialHint: partialReason,
	}
	if len(weeklyTrades) > 0 {
		facts.WeeklyAvgTradeInterval = avgIntervalFromCoverage(len(weeklyTrades), coverage, weekLookback)
	}
	if len(monthlyTrades) > 0 {
		facts.MonthlyAvgTradeInterval = avgIntervalFromCoverage(len(monthlyTrades), monthlyCoverage, monthLookback)
	}
	w.enrichProfitFromPositions(ctx, c.Wallet, distinct, monthlyDistinct, &facts)
	score := ScoreLuckySpike(facts, w.Params)

	if _, err := w.Store.InsertScore(ctx, walletID, toScoreRow(score)); err != nil && w.Log != nil {
		w.Log.Warn("lucky spike: persist score", "wallet", c.Wallet, "err", err)
	}
	metrics.Inc("wallet_scores_total{strategy=lucky_spike,result=" + classifyResult(score) + "}")

	if !score.Promote {
		return false, false, nil
	}

	sent := w.emitAlert(ctx, walletID, c, score)
	return true, sent, nil
}

func (w *LuckySpikeWorker) fetchWalletTrades(ctx context.Context, wallet string, oldestNeeded time.Time) ([]dataapi.Trade, string, error) {
	var (
		out           []dataapi.Trade
		partialReason string
		totalPages    int
	)
	pageSize := w.WalletTradePageSize
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 500
	}
	maxPages := w.WalletActivityMaxPages
	if maxPages <= 0 {
		maxPages = luckySpikeDefaultActivityMaxPage
	}
	startTs := oldestNeeded.Unix()
	endTs := int64(0)
	seen := make(map[string]struct{}, pageSize*maxPages)

	for totalPages < maxPages {
		windowRows := 0
		windowOldest := int64(1<<63 - 1)
		windowHitOffsetCap := false

		for offset := 0; offset <= luckySpikeActivityOffsetCap && totalPages < maxPages; offset += pageSize {
			batch, _, err := w.DataAPI.GetActivityPaginated(ctx, wallet, "TRADE", pageSize, offset, startTs, endTs, "TIMESTAMP", "DESC")
			if err != nil {
				return out, partialReason, err
			}
			totalPages++
			if len(batch) == 0 {
				break
			}
			windowRows += len(batch)
			for _, a := range batch {
				ts := a.Timestamp.Int64()
				if ts < windowOldest {
					windowOldest = ts
				}
				if ts < startTs {
					continue
				}
				if strings.ToUpper(strings.TrimSpace(a.Type)) != "TRADE" {
					continue
				}
				key := activityTradeKey(a)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, activityToTrade(a))
			}
			if windowOldest <= startTs {
				sortTradesAsc(out)
				return out, partialReason, nil
			}
			if len(batch) < pageSize {
				break
			}
			if offset == luckySpikeActivityOffsetCap {
				windowHitOffsetCap = true
				break
			}
		}

		if windowRows == 0 || windowOldest == int64(1<<63-1) {
			break
		}
		if windowOldest <= startTs || !windowHitOffsetCap {
			break
		}
		nextEnd := windowOldest - 1
		if nextEnd <= 0 || (endTs > 0 && nextEnd >= endTs) {
			partialReason = "ACTIVITY_CURSOR_STALLED"
			break
		}
		endTs = nextEnd
	}
	if totalPages >= maxPages && partialReason == "" {
		partialReason = "LOCAL_ACTIVITY_PAGE_CAP"
	}
	sortTradesAsc(out)
	return out, partialReason, nil
}

func activityToTrade(a dataapi.Activity) dataapi.Trade {
	return dataapi.Trade{
		TransactionHash: a.TransactionHash,
		ProxyWallet:     a.ProxyWallet,
		ConditionID:     a.ConditionID,
		Asset:           a.Asset,
		EventSlug:       a.EventSlug,
		Slug:            a.Slug,
		Title:           a.Title,
		Outcome:         a.Outcome,
		OutcomeIndex:    a.OutcomeIndex,
		Side:            a.Side,
		Price:           a.Price,
		Size:            a.Size,
		UsdcSize:        a.UsdcSize,
		Timestamp:       a.Timestamp,
		Pseudonym:       a.Pseudonym,
		Name:            a.Name,
	}
}

func activityTradeKey(a dataapi.Activity) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(a.ProxyWallet)),
		fmt.Sprintf("%d", a.Timestamp.Int64()),
		strings.ToUpper(strings.TrimSpace(a.Type)),
		strings.ToLower(strings.TrimSpace(a.TransactionHash)),
		strings.ToLower(strings.TrimSpace(a.ConditionID)),
		strings.ToLower(strings.TrimSpace(a.Asset)),
		strings.ToUpper(strings.TrimSpace(a.Side)),
		fmt.Sprintf("%.12g", a.Price.Float64()),
		fmt.Sprintf("%.12g", a.Size.Float64()),
		fmt.Sprintf("%.12g", a.UsdcSize.Float64()),
		fmt.Sprintf("%d", a.OutcomeIndex),
		strings.ToUpper(strings.TrimSpace(a.Outcome)),
	}, "|")
}

func sortTradesAsc(trades []dataapi.Trade) {
	sort.Slice(trades, func(i, j int) bool {
		return trades[i].Timestamp.Int64() < trades[j].Timestamp.Int64()
	})
}

func (w *LuckySpikeWorker) emitAlert(ctx context.Context, walletID string, c luckyCandidate, score ScoreResult) bool {
	if w.Router == nil {
		return false
	}
	snap := score.FeatureSnapshot

	weeklyTrades, _ := snap["weekly_trade_count"].(int)
	weeklyMkts, _ := snap["weekly_distinct_markets"].(int)
	weeklyCycles, _ := snap["weekly_realized_cycles"].(int)
	weeklyWins, _ := snap["weekly_profitable_cycles"].(int)
	weeklyLosses, _ := snap["weekly_losing_cycles"].(int)
	weeklyProfitPct, _ := snap["weekly_profit_pct"].(float64)
	monthlyProfitPct, _ := snap["monthly_profit_pct"].(float64)
	monthlyTrades, _ := snap["monthly_trade_count"].(int)
	monthlyCycles, _ := snap["monthly_realized_cycles"].(int)
	coverageHours, _ := snap["weekly_coverage_hours"].(float64)
	avgIntervalMins, _ := snap["weekly_avg_trade_interval_minutes"].(float64)
	monthlyAvgIntervalMins, _ := snap["monthly_avg_trade_interval_minutes"].(float64)
	avgNotional, _ := snap["weekly_avg_trade_notional"].(float64)
	dataQuality, _ := snap["data_quality"].(string)
	partialHint, _ := snap["trade_history_partial_hint"].(string)

	alert := alerts.LuckySpikeAlert{
		WalletShort:                    shortWallet(c.Wallet),
		WalletFull:                     c.Wallet,
		Pseudonym:                      c.Pseudonym,
		Score:                          score.Score,
		Confidence:                     score.Confidence,
		WeeklyTradeCount:               weeklyTrades,
		WeeklyDistinctMarkets:          weeklyMkts,
		WeeklyRealizedCycles:           weeklyCycles,
		WeeklyProfitableCycles:         weeklyWins,
		WeeklyLosingCycles:             weeklyLosses,
		WeeklyProfitPct:                weeklyProfitPct,
		MonthlyProfitPct:               monthlyProfitPct,
		MonthlyTradeCount:              monthlyTrades,
		MonthlyRealizedCycles:          monthlyCycles,
		MonthlyAvgTradeIntervalMinutes: monthlyAvgIntervalMins,
		WeeklyCoverageHours:            coverageHours,
		AvgTradeIntervalMinutes:        avgIntervalMins,
		AvgWeeklyTradeNotionalUSD:      avgNotional,
		TradeHistoryPartialHint:        partialHint,
		DataQuality:                    dataQuality,
		ReasonHumanized:                alerts.HumanizeReasons(score.ReasonCodes, 5),
		ReasonCodes:                    score.ReasonCodes,
	}

	year, week := time.Now().UTC().ISOWeek()
	weekBucket := fmt.Sprintf("%04d-W%02d", year, week)
	dedup := alerts.DedupKey(alerts.TypeLuckySpikeCandidate, walletID, weekBucket, ScoreVersion)
	alert.DedupKey = dedup

	snapMap := map[string]any(score.FeatureSnapshot)
	snapMap["wallet_id"] = walletID
	snapMap["wallet"] = c.Wallet
	snapMap["strategy"] = score.Strategy

	decision := postgres.AlertDecision{
		AlertType:         alerts.TypeLuckySpikeCandidate,
		EntityType:        "wallet",
		EntityID:          walletID,
		Severity:          "HIGH",
		ShouldSend:        true,
		UserAlertAllowed:  false,
		AdminAlertAllowed: true,
		ReasonCodes:       score.ReasonCodes,
		FeatureSnapshot:   snapMap,
		DedupKey:          dedup,
	}
	body := alerts.FormatLuckySpikeCandidate(alert, w.Links)
	out := w.Router.Route(ctx, decision, body, alerts.ChannelAdmin)
	if out.Err != nil {
		if w.Log != nil {
			w.Log.Warn("lucky spike alert send", "wallet", c.Wallet, "err", out.Err)
		}
		return false
	}
	return out.Sent
}

func tradesToReplay(trades []dataapi.Trade) []postgres.TradeForReplay {
	out := make([]postgres.TradeForReplay, 0, len(trades))
	for _, t := range trades {
		outcome := strings.ToUpper(strings.TrimSpace(t.Outcome))
		side := strings.ToUpper(strings.TrimSpace(t.Side))
		price := t.Price.Float64()
		size := t.Size.Float64()
		usdc := t.UsdcSize.Float64()
		if usdc == 0 {
			usdc = size * price
		}
		out = append(out, postgres.TradeForReplay{
			TransactionHash: t.TransactionHash,
			ConditionID:     t.ConditionID,
			Outcome:         outcome,
			Side:            side,
			Price:           price,
			Size:            size,
			UsdcSize:        usdc,
			Timestamp:       time.Unix(t.Timestamp.Int64(), 0).UTC(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	return out
}

func dataQualityFromPartial(partial string) string {
	switch partial {
	case "":
		return "complete"
	case "DATA_API_OFFSET_CAP_3000":
		return "partial_offset_cap"
	case "LOCAL_PAGE_CAP":
		return "partial_local_cap"
	case "LOCAL_ACTIVITY_PAGE_CAP":
		return "partial_activity_cap"
	case "ACTIVITY_CURSOR_STALLED":
		return "partial_activity_cursor"
	default:
		return "proxy"
	}
}

func mapToSlice(m map[string]luckyCandidate) []luckyCandidate {
	out := make([]luckyCandidate, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Wallet < out[j].Wallet })
	return out
}

func shortWallet(w string) string {
	w = strings.TrimSpace(w)
	if len(w) <= 14 {
		return w
	}
	return w[:8] + "..." + w[len(w)-6:]
}
