package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type AlertDecision struct {
	ID                string
	AlertType         string
	EntityType        string
	EntityID          string
	Severity          string
	ShouldSend        bool
	UserAlertAllowed  bool
	AdminAlertAllowed bool
	ReasonCodes       []string
	MissingData       []string
	FeatureSnapshot   map[string]any
	DedupKey          string
}

// InsertAlertDecision persists decision before Telegram send.
// Returns (id, inserted_new). On dedup_key conflict returns existing id and
// inserted_new=false — the caller MUST treat this as "already routed".
func (s *Store) InsertAlertDecision(ctx context.Context, d AlertDecision) (string, bool, error) {
	snap, err := json.Marshal(d.FeatureSnapshot)
	if err != nil {
		return "", false, err
	}
	const q = `
		INSERT INTO alert_decisions
		    (alert_type, entity_type, entity_id, severity, should_send,
		     user_alert_allowed, admin_alert_allowed, reason_codes, missing_data,
		     feature_snapshot, dedup_key)
		VALUES
		    ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11)
		ON CONFLICT (dedup_key) DO NOTHING
		RETURNING id::text`
	var id string
	err = s.Pool.QueryRow(ctx, q,
		d.AlertType, d.EntityType, d.EntityID, d.Severity, d.ShouldSend,
		d.UserAlertAllowed, d.AdminAlertAllowed,
		reasonsArr(d.ReasonCodes), reasonsArr(d.MissingData), string(snap), d.DedupKey,
	).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// existed already
		err2 := s.Pool.QueryRow(ctx, `SELECT id::text FROM alert_decisions WHERE dedup_key=$1`, d.DedupKey).Scan(&id)
		if err2 != nil {
			return "", false, err2
		}
		return id, false, nil
	}
	return "", false, err
}

type TelegramDelivery struct {
	AlertDecisionID   string
	ChatID            string
	Status            string // "ok" | "failed" | "skipped"
	TelegramMessageID string
	Error             string
	SentAt            time.Time
	Body              string
	Attempt           int
	NextAttemptAt     time.Time
}

func (s *Store) InsertTelegramDelivery(ctx context.Context, d TelegramDelivery) error {
	if d.Attempt == 0 {
		d.Attempt = 1
	}
	const q = `
		INSERT INTO telegram_deliveries
		    (alert_decision_id, chat_id, status, telegram_message_id, error,
		     sent_at, body, attempt, next_attempt_at)
		VALUES
		    ($1::uuid, $2, $3, NULLIF($4,''), NULLIF($5,''),
		     $6, NULLIF($7,''), $8, $9)`
	_, err := s.Pool.Exec(ctx, q,
		d.AlertDecisionID, d.ChatID, d.Status, d.TelegramMessageID, d.Error,
		nilIfZeroTime(d.SentAt), d.Body, d.Attempt, nilIfZeroTime(d.NextAttemptAt))
	return err
}
