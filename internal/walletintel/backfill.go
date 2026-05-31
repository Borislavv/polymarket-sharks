package walletintel

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// HistoryBackfillWorker drains a wallet's full /trades and closed-positions
// history from the Polymarket Data API and persists it for v4 shark scoring.
// Bounded concurrency; respects context cancellation; retries are delegated to
// the HTTP layer's rate limiter (429 backoff lives there).
//
// The worker is idle-tolerant: if there are no wallets needing backfill, it
// simply waits for the next tick.
type HistoryBackfillWorker struct {
	DataAPI            *dataapi.Client
	Store              *postgres.Store
	Log                *slog.Logger
	Interval           time.Duration
	BatchSize          int                          // max wallets per cycle
	Concurrency        int                          // simultaneous wallets in flight
	TradePageSize      int                          // /trades page size
	ClosedPageSize     int                          // /closed-positions page size
	MaxTradePages      int                          // safety cap
	MaxClosedPages     int                          // safety cap
	OnWalletBackfilled func(walletID, proxy string) // optional re-score trigger
	// SnapshotCache holds the latest closed-position fingerprint per
	// (wallet, condition, outcome). Identical payloads are skipped at write
	// time so the DB only receives rows whose state actually changed. nil is
	// permitted — every payload then takes the full upsert path.
	SnapshotCache *ClosedPositionSnapshotCache
}

func (w *HistoryBackfillWorker) defaults() {
	if w.Interval <= 0 {
		w.Interval = 2 * time.Minute
	}
	if w.BatchSize <= 0 {
		w.BatchSize = 30
	}
	if w.Concurrency <= 0 {
		w.Concurrency = 3
	}
	if w.TradePageSize <= 0 {
		w.TradePageSize = 500
	}
	if w.ClosedPageSize <= 0 {
		w.ClosedPageSize = 500
	}
	if w.MaxTradePages <= 0 {
		w.MaxTradePages = 20
	}
	if w.MaxClosedPages <= 0 {
		w.MaxClosedPages = 20
	}
}

// Run is the worker loop. Cancellable via ctx.
func (w *HistoryBackfillWorker) Run(ctx context.Context) error {
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

func (w *HistoryBackfillWorker) runOnce(ctx context.Context) {
	targets, err := w.Store.ListWalletsNeedingBackfill(ctx, w.BatchSize)
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("backfill list targets", "err", err)
		}
		return
	}
	if len(targets) == 0 {
		return
	}
	if w.Log != nil {
		w.Log.Info("backfill cycle started", "wallets", len(targets), "concurrency", w.Concurrency)
	}
	if w.SnapshotCache != nil {
		metrics.SetGauge("wallet_closed_positions_cache_entries", int64(w.SnapshotCache.Len()))
	}
	sem := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	for _, t := range targets {
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(tgt postgres.WalletBackfillTarget) {
			defer wg.Done()
			defer func() { <-sem }()
			w.backfillOne(ctx, tgt)
		}(t)
	}
	wg.Wait()
}

// BackfillOne is a public entry point used by the dry-run CLI and tests.
// It returns the final backfill record for callers who want immediate stats.
func (w *HistoryBackfillWorker) BackfillOne(ctx context.Context, walletID, proxy string) (postgres.BackfillRow, error) {
	w.defaults()
	row := w.backfillOne(ctx, postgres.WalletBackfillTarget{WalletID: walletID, ProxyWallet: proxy})
	return row, nil
}

func (w *HistoryBackfillWorker) backfillOne(ctx context.Context, t postgres.WalletBackfillTarget) postgres.BackfillRow {
	start := time.Now()
	res := postgres.BackfillRow{WalletID: t.WalletID}

	tradesFetched, tradesPersisted, tradesComplete, tradesPartialReason, tradesErr := w.drainTrades(ctx, t.WalletID, t.ProxyWallet)
	res.TradesFetched = tradesFetched
	res.TradesComplete = tradesComplete

	closedFetched, closedComplete, closedPartialReason, closedErr := w.drainClosedPositions(ctx, t.WalletID, t.ProxyWallet)
	res.ClosedPositionsFetched = closedFetched
	res.ClosedPositionsComplete = closedComplete

	// Realized-cycle reconstruction from the wallet's full trade history.
	// PnL here is trading profit (exit_price vs weighted avg entry), NEVER
	// inferred from market resolution.
	var reconstructedCycles, persistedCycles int
	if tradesPersisted > 0 || tradesFetched > 0 {
		if replayTrades, err := w.Store.LatestTradesForWallet(ctx, t.WalletID, 0); err == nil {
			cycles := ReconstructRealizedTrades(t.WalletID, replayTrades)
			reconstructedCycles = len(cycles)
			if reconstructedCycles > 0 {
				if n, err := w.Store.InsertRealizedTrades(ctx, cycles); err == nil {
					persistedCycles = n
					metrics.Add("wallet_realized_trades_persisted_total", int64(n))
				}
			}
		}
	}

	now := time.Now()
	res.LastBackfilledAt = &now
	// Real errors land in LastError. Partial-reasons are NOT errors — they
	// describe legitimate data-source limits and live in raw_stats.
	if tradesErr != nil {
		res.LastError = "trades: " + tradesErr.Error()
	}
	if closedErr != nil {
		if res.LastError != "" {
			res.LastError += " | "
		}
		res.LastError += "closed: " + closedErr.Error()
	}
	res.RawStats = map[string]any{
		"duration_ms":                     time.Since(start).Milliseconds(),
		"trades_partial_reason":           tradesPartialReason,
		"closed_positions_partial_reason": closedPartialReason,
		"available_trades_count":          tradesFetched,
		"trades_persisted_count":          tradesPersisted,
		"available_closed_positions":      closedFetched,
		"reconstructed_realized_cycles":   reconstructedCycles,
		"realized_cycles_inserted":        persistedCycles,
		"data_quality":                    deriveDataQuality(tradesComplete, closedComplete, tradesPartialReason, closedPartialReason, closedFetched),
	}
	if err := w.Store.UpsertBackfillRecord(ctx, res); err != nil {
		if w.Log != nil {
			w.Log.Warn("backfill upsert", "wallet", t.ProxyWallet, "err", err)
		}
	}
	status := "complete"
	switch {
	case res.LastError != "":
		status = "error"
	case !res.TradesComplete || !res.ClosedPositionsComplete:
		status = "partial"
	}
	metrics.Inc("wallet_history_backfill_total{status=" + status + "}")
	metrics.Add("wallet_closed_positions_fetched_total", int64(res.ClosedPositionsFetched))
	metrics.Add("wallet_trades_backfilled_total", int64(res.TradesFetched))
	if w.Log != nil {
		w.Log.Info("wallet history backfilled",
			"wallet", t.ProxyWallet,
			"trades_fetched", res.TradesFetched,
			"closed_positions_fetched", res.ClosedPositionsFetched,
			"trades_complete", res.TradesComplete,
			"closed_positions_complete", res.ClosedPositionsComplete,
			"trades_partial_reason", tradesPartialReason,
			"closed_positions_partial_reason", closedPartialReason,
			"duration", time.Since(start).String(),
			"error", res.LastError)
	}
	// Trigger rescore on any usable closed history (complete OR partial with
	// real rows). The scoring layer modulates confidence — it does not need
	// "complete" to evaluate a wallet whose 1500 closed positions are real.
	hasUsableClosedHistory := res.ClosedPositionsFetched > 0 || res.ClosedPositionsComplete
	if hasUsableClosedHistory && w.OnWalletBackfilled != nil {
		w.OnWalletBackfilled(t.WalletID, t.ProxyWallet)
	}
	return res
}

// deriveDataQuality projects partial-reasons + counts into a single string
// label persisted in raw_stats and re-emitted in the wallet_scores feature
// snapshot.  Values: complete | partial_offset_cap | partial_safety_cap |
// partial_local_cap | proxy | missing.
func deriveDataQuality(tradesComplete, closedComplete bool, tradesReason, closedReason string, closedFetched int) string {
	if tradesComplete && closedComplete {
		return "complete"
	}
	if closedReason == "SAFETY_CAP_HIT" {
		return "partial_safety_cap"
	}
	if tradesReason == "DATA_API_OFFSET_CAP_3000" {
		return "partial_offset_cap"
	}
	if !closedComplete && closedReason == "LOCAL_PAGE_CAP" {
		return "partial_local_cap"
	}
	if closedFetched == 0 {
		return "missing"
	}
	return "proxy"
}

// drainTrades pages /trades for the wallet AND persists each trade into
// wallet_trades so the realized-cycle reconstruction has a stable input.
//
// Returns: (totalFetched, totalPersisted, complete, partialReason, err)
//   - complete=true: drain reached natural end (short page)
//   - partialReason="LOCAL_PAGE_CAP": hit MaxTradePages safety cap
//   - partialReason="DATA_API_OFFSET_CAP_3000": Polymarket /trades capped at offset 3000
//   - err: only for unrecoverable failures, NOT for partial-data signals
func (w *HistoryBackfillWorker) drainTrades(ctx context.Context, walletID, proxy string) (int, int, bool, string, error) {
	total := 0
	persisted := 0
	for page := 0; page < w.MaxTradePages; page++ {
		batch, _, err := w.DataAPI.GetTradesPaginated(ctx, proxy, "", false, w.TradePageSize, page*w.TradePageSize)
		if err != nil {
			// Polymarket caps /trades at offset 3000. Treat as partial,
			// not fatal: the trades we already fetched are valid data.
			if strings.Contains(err.Error(), "max historical activity offset") {
				return total, persisted, false, "DATA_API_OFFSET_CAP_3000", nil
			}
			return total, persisted, false, "", err
		}
		total += len(batch)
		// Persist every trade we receive so reconstruction has the full
		// pre-resolution sell history.
		for _, t := range batch {
			outcome := strings.ToUpper(strings.TrimSpace(t.Outcome))
			side := strings.ToUpper(strings.TrimSpace(t.Side))
			usd := t.UsdcSize.Float64()
			if usd == 0 {
				usd = t.Size.Float64() * t.Price.Float64()
			}
			row := postgres.WalletTrade{
				TransactionHash: t.TransactionHash,
				WalletID:        walletID,
				ConditionID:     t.ConditionID,
				EventSlug:       t.EventSlug,
				Outcome:         outcome,
				Side:            side,
				Direction:       deriveDirection(outcome, side),
				Price:           t.Price.Float64(),
				Size:            t.Size.Float64(),
				UsdcSize:        usd,
				Timestamp:       time.Unix(t.Timestamp.Int64(), 0).UTC(),
				Source:          "data-api/trades-backfill",
			}
			if _, inserted, err := w.Store.InsertWalletTrade(ctx, row); err == nil && inserted {
				persisted++
			}
		}
		if len(batch) < w.TradePageSize {
			return total, persisted, true, "", nil
		}
	}
	return total, persisted, false, "LOCAL_PAGE_CAP", nil
}

// deriveDirection resolves a Data API outcome+side to the canonical direction
// label. It uses DirectionOf so categorical outcomes (team names, UP/DOWN etc.)
// are correctly mapped to OUTCOME_BUY / OUTCOME_SELL rather than silently
// dropped. Returns "" on missing input (best-effort; does not block backfill).
func deriveDirection(outcome, side string) string {
	res, err := DirectionOf(outcome, side)
	if err != nil {
		return ""
	}
	return string(res.Direction)
}

// drainClosedPositions pages /closed-positions for the wallet and persists
// snapshot rows. Reaches a clean stop when the API returns a short page; if
// the safety cap is hit, returns the partial reason so scoring can lower
// confidence rather than reject the wallet outright.
//
// Returns: (total, complete, partialReason, err)
func (w *HistoryBackfillWorker) drainClosedPositions(ctx context.Context, walletID, proxy string) (int, bool, string, error) {
	total := 0
	for page := 0; page < w.MaxClosedPages; page++ {
		out, err := w.DataAPI.GetClosedPositionsPaginated(ctx, proxy, w.ClosedPageSize, page*w.ClosedPageSize)
		if err != nil {
			return total, false, "", err
		}
		if len(out.Items) > 0 {
			rows := make([]postgres.ClosedPositionRow, 0, len(out.Items))
			for _, it := range out.Items {
				var closedAt *time.Time
				if it.ClosedAt != "" {
					if t, err := parseClosedAt(it.ClosedAt); err == nil {
						closedAt = &t
					}
				}
				rows = append(rows, postgres.ClosedPositionRow{
					WalletID:           walletID,
					ConditionID:        it.ConditionID,
					EventSlug:          it.EventSlug,
					Outcome:            it.Outcome,
					OutcomeIndex:       it.OutcomeIndex,
					TotalBought:        it.TotalBought,
					RealizedPnL:        it.RealizedPnL,
					AvgPrice:           it.AvgPrice,
					CurrentValue:       it.CurrentValue,
					PercentPnL:         it.PercentPnL,
					PercentRealizedPnL: it.PercentRealizedPnL,
					SizeAtObservation:  it.Size,
					IsClosed:           it.IsClosed,
					ClosedAt:           closedAt,
					Raw:                []byte(it.Raw),
				})
			}
			plan := w.SnapshotCache.Plan(walletID, rows)
			if len(plan.Changed) > 0 {
				res, err := w.Store.UpsertClosedPositions(ctx, walletID, plan.Changed)
				if err != nil {
					return total, false, "", err
				}
				w.SnapshotCache.Commit(walletID, plan.Changed)
				metrics.Add("wallet_closed_positions_upserts_total{result=insert}", int64(res.Inserted))
				metrics.Add("wallet_closed_positions_upserts_total{result=update}", int64(res.Updated))
				metrics.Add("wallet_closed_positions_payload_changed_total", int64(len(plan.Changed)))
			}
			if len(plan.Unchanged) > 0 {
				if _, err := w.Store.TouchClosedPositionsLastSeen(ctx, walletID, plan.Unchanged); err != nil {
					return total, false, "", err
				}
				metrics.Add("wallet_closed_positions_upserts_total{result=skipped}", int64(len(plan.Unchanged)))
			}
			total += len(rows)
		}
		if !out.HasMore || len(out.Items) < w.ClosedPageSize {
			return total, true, "", nil
		}
	}
	// Safety cap reached — partial, NOT fatal: every closed position we did
	// fetch is real data. Scoring decides what to do with reduced confidence.
	return total, false, "SAFETY_CAP_HIT", nil
}

func parseClosedAt(s string) (time.Time, error) {
	// Polymarket emits ISO-8601 in some endpoints and unix-seconds in others.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	return time.Parse(time.RFC3339, s)
}
