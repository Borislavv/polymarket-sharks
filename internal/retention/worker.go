package retention

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

type Worker struct {
	Store *postgres.Store
	Log   *slog.Logger

	Enabled         bool
	Interval        time.Duration
	PerTableTimeout time.Duration
	BatchSize       int

	WalletClosedPositionsMaxRows int64
	MarketPriceSamplesMaxRows    int64
	HolderSnapshotsMaxRows       int64
	CandidateEvidenceMaxRows     int64
	WalletScoresMaxRows          int64

	closedPositionsDedupCursor      int64
	closedPositionsDedupPassDeleted int64
}

func (w *Worker) defaults() {
	if w.Interval <= 0 {
		w.Interval = time.Minute
	}
	if w.PerTableTimeout <= 0 {
		w.PerTableTimeout = 45 * time.Second
	}
	if w.BatchSize <= 0 {
		w.BatchSize = 50_000
	}
}

func (w *Worker) Run(ctx context.Context) error {
	w.defaults()
	if !w.Enabled {
		return nil
	}
	w.runOnce(ctx)
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			w.runOnce(ctx)
		}
	}
}

func (w *Worker) runOnce(ctx context.Context) {
	if w.Store == nil {
		return
	}
	start := time.Now()
	var totalDeleted int64

	deletedDup, dedupComplete := w.runClosedPositionsDedup(ctx)
	totalDeleted += deletedDup

	// Only enforce the wallet_closed_positions row cap after a full cursor
	// pass found no duplicates. This prioritizes latest-row correctness while
	// the legacy append-only backlog is still being collapsed.
	if dedupComplete {
		totalDeleted += w.runCap(ctx, "wallet_closed_positions", w.WalletClosedPositionsMaxRows, func(c context.Context, limit int) (int64, error) {
			return w.Store.PruneWalletClosedPositionsOldestNonWatchlist(c, limit)
		})
	}
	totalDeleted += w.runCap(ctx, "market_price_samples", w.MarketPriceSamplesMaxRows, func(c context.Context, limit int) (int64, error) {
		return w.Store.PruneMarketPriceSamplesOldest(c, limit)
	})
	totalDeleted += w.runCap(ctx, "holder_snapshots", w.HolderSnapshotsMaxRows, func(c context.Context, limit int) (int64, error) {
		return w.Store.PruneHolderSnapshotsOldest(c, limit)
	})
	totalDeleted += w.runCap(ctx, "wallet_candidate_evidence", w.CandidateEvidenceMaxRows, func(c context.Context, limit int) (int64, error) {
		return w.Store.PruneCandidateEvidenceOldestNonWatchlist(c, limit)
	})
	totalDeleted += w.runCap(ctx, "wallet_scores", w.WalletScoresMaxRows, func(c context.Context, limit int) (int64, error) {
		return w.Store.PruneWalletScoresOldestNonWatchlist(c, limit)
	})

	metrics.Inc("retention_runs_total")
	metrics.SetGauge("retention_last_run_rows_deleted", totalDeleted)
	metrics.SetGauge("retention_last_success_unix", time.Now().Unix())
	if w.Log != nil {
		w.Log.Info("retention cycle completed",
			"rows_deleted", totalDeleted,
			"duration", time.Since(start).String())
	}
}

func (w *Worker) runClosedPositionsDedup(ctx context.Context) (int64, bool) {
	runCtx, cancel := context.WithTimeout(ctx, w.PerTableTimeout)
	defer cancel()
	res, err := w.Store.PruneWalletClosedPositionDuplicateBatch(runCtx, w.BatchSize, w.closedPositionsDedupCursor)
	if err != nil {
		w.recordError("wallet_closed_positions", "dedup", err)
		return 0, false
	}
	w.closedPositionsDedupCursor = res.MaxScannedID
	w.closedPositionsDedupPassDeleted += res.RowsDeleted
	metrics.Add(metricName("retention_deleted_rows_total", "wallet_closed_positions", "dedup"), res.RowsDeleted)
	metrics.SetGauge(metricName("retention_last_run_rows_deleted", "wallet_closed_positions", "dedup"), res.RowsDeleted)
	metrics.SetGauge(metricName("retention_last_scan_rows", "wallet_closed_positions", "dedup"), res.RowsScanned)
	metrics.SetGauge(metricName("retention_scan_cursor", "wallet_closed_positions", "dedup"), res.MaxScannedID)
	if res.RowsDeleted > 0 && w.Log != nil {
		w.Log.Info("retention swept",
			"table", "wallet_closed_positions",
			"reason", "dedup",
			"rows_scanned", res.RowsScanned,
			"rows_deleted", res.RowsDeleted,
			"cursor", res.MaxScannedID)
	}
	if res.RowsScanned >= int64(w.BatchSize) {
		return res.RowsDeleted, false
	}

	passDeleted := w.closedPositionsDedupPassDeleted
	w.closedPositionsDedupCursor = 0
	w.closedPositionsDedupPassDeleted = 0
	if w.Log != nil {
		w.Log.Info("retention closed-position dedup pass completed",
			"rows_deleted_in_pass", passDeleted)
	}
	return res.RowsDeleted, passDeleted == 0
}

func (w *Worker) runCap(ctx context.Context, table string, maxRows int64, deleteFn func(context.Context, int) (int64, error)) int64 {
	if maxRows <= 0 {
		return 0
	}
	estimateCtx, cancel := context.WithTimeout(ctx, w.PerTableTimeout)
	estimate, err := w.Store.EstimateTableRows(estimateCtx, table)
	cancel()
	if err != nil {
		w.recordError(table, "estimate", err)
		return 0
	}
	metrics.SetGauge(metricName("retention_estimated_rows", table, "rowcap"), estimate)
	if estimate <= maxRows {
		return 0
	}
	overage := estimate - maxRows
	limit := w.BatchSize
	if overage < int64(limit) {
		limit = int(overage)
	}
	return w.runDelete(ctx, table, "rowcap", deleteFnWithLimit(deleteFn, limit))
}

func (w *Worker) runDelete(ctx context.Context, table, reason string, deleteFn func(context.Context, int) (int64, error)) int64 {
	if w.BatchSize <= 0 {
		return 0
	}
	runCtx, cancel := context.WithTimeout(ctx, w.PerTableTimeout)
	defer cancel()
	deleted, err := deleteFn(runCtx, w.BatchSize)
	if err != nil {
		w.recordError(table, reason, err)
		return 0
	}
	metrics.Add(metricName("retention_deleted_rows_total", table, reason), deleted)
	metrics.SetGauge(metricName("retention_last_run_rows_deleted", table, reason), deleted)
	if deleted > 0 && w.Log != nil {
		w.Log.Info("retention swept",
			"table", table,
			"reason", reason,
			"rows_deleted", deleted)
	}
	return deleted
}

func deleteFnWithLimit(deleteFn func(context.Context, int) (int64, error), limit int) func(context.Context, int) (int64, error) {
	return func(ctx context.Context, _ int) (int64, error) {
		return deleteFn(ctx, limit)
	}
}

func (w *Worker) recordError(table, reason string, err error) {
	metrics.Inc(metricName("retention_run_failed_total", table, reason))
	if w.Log != nil {
		w.Log.Warn("retention sweep failed", "table", table, "reason", reason, "err", err)
	}
}

func metricName(name, table, reason string) string {
	return fmt.Sprintf("%s{table=%s,reason=%s}", name, table, reason)
}
