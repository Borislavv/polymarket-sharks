package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
	"github.com/Borislavv/polymarket-sharks/internal/telegram"
)

// RetryWorker re-sends failed Telegram deliveries that have not yet
// reached MaxAttempts. Stored alongside the persisted body in the
// telegram_deliveries row so retry needs no upstream context.
type RetryWorker struct {
	Store       *postgres.Store
	Telegram    *telegram.Client
	Log         *slog.Logger
	Interval    time.Duration
	MaxAttempts int
	BatchSize   int
}

func (w *RetryWorker) Run(ctx context.Context) error {
	if w.Interval <= 0 {
		w.Interval = 30 * time.Second
	}
	if w.MaxAttempts <= 0 {
		w.MaxAttempts = 5
	}
	if w.BatchSize <= 0 {
		w.BatchSize = 50
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

func (w *RetryWorker) runOnce(ctx context.Context) {
	if w.Telegram == nil {
		return
	}
	failed, err := w.Store.ListFailedDeliveriesDue(ctx, w.MaxAttempts, w.BatchSize)
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("retry worker list", "err", err)
		}
		return
	}
	for _, d := range failed {
		// already-succeeded short-circuit: if any successful delivery exists
		// for the same decision (e.g. another worker raced), skip.
		ok, err := w.Store.HasSuccessfulDelivery(ctx, d.AlertDecisionID)
		if err == nil && ok {
			_ = w.Store.UpdateDeliveryStatus(ctx, d.DeliveryID, "skipped")
			continue
		}
		body, err := w.loadBody(ctx, d.DeliveryID)
		if err != nil || body == "" {
			// nothing to send
			_ = w.Store.MarkDeliveryRetry(ctx, d.DeliveryID, time.Now().Add(w.backoff(d.Attempt+1)), "missing body")
			continue
		}
		msgID, err := w.Telegram.SendMessage(ctx, d.ChatID, body)
		if err != nil {
			next := time.Now().Add(w.backoff(d.Attempt + 1))
			if err2 := w.Store.MarkDeliveryRetry(ctx, d.DeliveryID, next, err.Error()); err2 != nil && w.Log != nil {
				w.Log.Warn("retry mark", "err", err2)
			}
			metrics.Inc("telegram_retry_failures_total")
			continue
		}
		if err := w.Store.MarkDeliverySucceeded(ctx, d.DeliveryID, msgID); err != nil && w.Log != nil {
			w.Log.Warn("retry mark success", "err", err)
		}
		metrics.Inc("telegram_retry_successes_total")
	}
}

func (w *RetryWorker) loadBody(ctx context.Context, deliveryID string) (string, error) {
	var body string
	err := w.Store.Pool.QueryRow(ctx,
		`SELECT COALESCE(body,'') FROM telegram_deliveries WHERE id=$1::uuid`,
		deliveryID).Scan(&body)
	if err != nil {
		return "", fmt.Errorf("retry load body: %w", err)
	}
	return body, nil
}

func (w *RetryWorker) backoff(attempt int) time.Duration {
	// 30s, 1m, 2m, 4m, 8m, capped at 15m
	base := 30 * time.Second
	for i := 1; i < attempt; i++ {
		base *= 2
		if base >= 15*time.Minute {
			return 15 * time.Minute
		}
	}
	return base
}
