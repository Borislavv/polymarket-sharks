package walletintel

import (
	"context"
	"log/slog"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// ReconcileWorker periodically re-evaluates active sharks against current
// hard gates and demotes wallets that no longer qualify. Cheap (operates
// on stored feature_snapshot, no Data API calls).
type ReconcileWorker struct {
	Store    *postgres.Store
	Params   SharkParams
	Log      *slog.Logger
	Interval time.Duration
}

func (w *ReconcileWorker) Run(ctx context.Context) error {
	if w.Interval <= 0 {
		w.Interval = 10 * time.Minute
	}
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			_, _ = ReconcileFailedSharks(ctx, w.Store, w.Params, w.Log)
			_, _ = ReconcileInsiderStreaks(ctx, w.Store, w.Log)
		}
	}
}
