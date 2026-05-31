package postgres

import (
	"context"
	"time"
)

type FailedDelivery struct {
	DeliveryID      string
	AlertDecisionID string
	ChatID          string
	Attempt         int
	NextAttemptAt   time.Time
	Error           string
}

// ListFailedDeliveriesDue returns failed deliveries whose next_attempt_at
// has elapsed and that have not exceeded maxAttempts.
func (s *Store) ListFailedDeliveriesDue(ctx context.Context, maxAttempts, limit int) ([]FailedDelivery, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, alert_decision_id::text, chat_id, attempt,
		       COALESCE(next_attempt_at, now()), COALESCE(error,'')
		FROM telegram_deliveries
		WHERE status = 'failed'
		  AND attempt < $1
		  AND (next_attempt_at IS NULL OR next_attempt_at <= now())
		ORDER BY created_at ASC
		LIMIT $2
	`, maxAttempts, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FailedDelivery
	for rows.Next() {
		var d FailedDelivery
		if err := rows.Scan(&d.DeliveryID, &d.AlertDecisionID, &d.ChatID, &d.Attempt, &d.NextAttemptAt, &d.Error); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) MarkDeliveryRetry(ctx context.Context, deliveryID string, nextAt time.Time, errMsg string) error {
	const q = `
		UPDATE telegram_deliveries
		SET attempt = attempt + 1,
		    next_attempt_at = $2,
		    error = $3
		WHERE id = $1::uuid`
	_, err := s.Pool.Exec(ctx, q, deliveryID, nextAt, errMsg)
	return err
}

func (s *Store) MarkDeliverySucceeded(ctx context.Context, deliveryID, messageID string) error {
	const q = `
		UPDATE telegram_deliveries
		SET status = 'ok',
		    telegram_message_id = $2,
		    sent_at = now(),
		    error = NULL,
		    next_attempt_at = NULL
		WHERE id = $1::uuid`
	_, err := s.Pool.Exec(ctx, q, deliveryID, messageID)
	return err
}

type AlertDecisionView struct {
	ID        string
	AlertType string
	Severity  string
	Body      string
	DedupKey  string
}

// HasSuccessfulDelivery returns true if any delivery for this decision
// is already 'ok'. Used by retry worker to skip already-sent decisions.
func (s *Store) HasSuccessfulDelivery(ctx context.Context, decisionID string) (bool, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM telegram_deliveries
		WHERE alert_decision_id = $1::uuid AND status = 'ok'
	`, decisionID).Scan(&n)
	return n > 0, err
}

func (s *Store) UpdateDeliveryStatus(ctx context.Context, deliveryID, status string) error {
	const q = `UPDATE telegram_deliveries SET status=$2 WHERE id=$1::uuid`
	_, err := s.Pool.Exec(ctx, q, deliveryID, status)
	return err
}
