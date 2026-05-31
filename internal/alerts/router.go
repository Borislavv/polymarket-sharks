package alerts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/logfields"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
	"github.com/Borislavv/polymarket-sharks/internal/telegram"
)

// Router is the ONLY component allowed to send Telegram messages.
// All workers MUST go through Router.
type Router struct {
	Store       *postgres.Store
	Telegram    *telegram.Client
	Links       LinkBuilder
	Log         *slog.Logger
	ChatAdmin   string
	ChatBets    string
	ChatCluster string
	ChatNews    string

	// AlertingEnabled is a global kill-switch. When false, decisions and
	// deliveries are still persisted (status="skipped") for audit and the
	// Telegram API is never called. Use for staging/dry-run.
	AlertingEnabled bool
}

func NewRouter(store *postgres.Store, tg *telegram.Client, links LinkBuilder, log *slog.Logger,
	chatAdmin, chatBets, chatCluster, chatNews string) *Router {
	return &Router{
		Store: store, Telegram: tg, Links: links, Log: log,
		ChatAdmin: chatAdmin, ChatBets: chatBets, ChatCluster: chatCluster, ChatNews: chatNews,
		AlertingEnabled: true,
	}
}

// Outcome of a routing attempt — useful for tests and observability.
type Outcome struct {
	DecisionID    string
	DecisionNew   bool
	Sent          bool
	ChatID        string
	TelegramMsgID string
	Err           error
	SkipReason    string
}

// Route handles one alert end-to-end:
//  1. construct dedup key (caller-provided)
//  2. persist alert_decisions row (idempotent on dedup_key)
//  3. if decision was newly inserted AND user_alert_allowed → send
//  4. record telegram_deliveries row on success or failure
//
// On step-3 send failure the decision survives in DB — caller can retry
// later without losing audit evidence.
func (r *Router) Route(ctx context.Context, decision postgres.AlertDecision, body string, channel string) Outcome {
	id, isNew, err := r.Store.InsertAlertDecision(ctx, decision)
	if err != nil {
		return Outcome{Err: fmt.Errorf("router: persist decision: %w", err)}
	}
	out := Outcome{DecisionID: id, DecisionNew: isNew}
	if isNew && r.Log != nil {
		r.Log.Info("alert decision created",
			"alert_type", decision.AlertType,
			"severity", decision.Severity,
			"should_send", decision.ShouldSend,
			"dedup_key_short", logfields.Short(decision.DedupKey))
	}

	if !isNew {
		out.SkipReason = "duplicate_dedup_key"
		if r.Log != nil {
			r.Log.Info("telegram alert skipped",
				"alert_type", decision.AlertType,
				"reason", "duplicate_dedup_key")
		}
		return out
	}
	if !decision.UserAlertAllowed && channel != ChannelAdmin {
		// fall back to admin if admin_alert_allowed
		if decision.AdminAlertAllowed {
			channel = ChannelAdmin
		} else {
			out.SkipReason = "no_channel_allowed"
			if r.Log != nil {
				r.Log.Info("telegram alert skipped",
					"alert_type", decision.AlertType,
					"reason", "no_channel_allowed")
			}
			return out
		}
	}
	if !decision.ShouldSend {
		out.SkipReason = "should_send_false"
		if r.Log != nil {
			r.Log.Info("telegram alert skipped",
				"alert_type", decision.AlertType,
				"reason", "should_send_false")
		}
		return out
	}

	chat := r.chatFor(channel)
	if chat == "" {
		out.SkipReason = "no_chat_configured"
		return out
	}
	out.ChatID = chat
	if r.Telegram == nil {
		out.SkipReason = "telegram_disabled"
		return out
	}
	if !r.AlertingEnabled {
		_ = r.Store.InsertTelegramDelivery(ctx, postgres.TelegramDelivery{
			AlertDecisionID: id,
			ChatID:          chat,
			Status:          "skipped",
			Body:            body,
			Attempt:         1,
		})
		out.SkipReason = "alerting_disabled"
		if r.Log != nil {
			r.Log.Info("telegram alert skipped",
				"alert_type", decision.AlertType,
				"reason", "alerting_disabled")
		}
		return out
	}

	tgMsgID, sendErr := r.Telegram.SendMessage(ctx, chat, body)
	delivery := postgres.TelegramDelivery{
		AlertDecisionID:   id,
		ChatID:            chat,
		Status:            "ok",
		TelegramMessageID: tgMsgID,
		SentAt:            time.Now(),
		Body:              body,
		Attempt:           1,
	}
	if sendErr != nil {
		delivery.Status = "failed"
		delivery.Error = sendErr.Error()
		delivery.SentAt = time.Time{}
		delivery.NextAttemptAt = time.Now().Add(30 * time.Second)
		if err := r.Store.InsertTelegramDelivery(ctx, delivery); err != nil && r.Log != nil {
			r.Log.Error("delivery row insert failed", "err", err)
		}
		if r.Log != nil {
			r.Log.Warn("telegram alert failed",
				"alert_type", decision.AlertType,
				"error", sendErr.Error())
		}
		out.Err = sendErr
		return out
	}
	if err := r.Store.InsertTelegramDelivery(ctx, delivery); err != nil && r.Log != nil {
		r.Log.Error("delivery row insert failed", "err", err)
	}
	if r.Log != nil {
		r.Log.Info("telegram alert sent",
			"alert_type", decision.AlertType,
			"chat_id", chat,
			"message_id", tgMsgID)
	}
	out.Sent = true
	out.TelegramMsgID = tgMsgID
	return out
}

func (r *Router) chatFor(channel string) string {
	switch channel {
	case ChannelAdmin:
		return r.ChatAdmin
	case ChannelBets:
		return r.ChatBets
	case ChannelClusters:
		return r.ChatCluster
	case ChannelNews:
		return r.ChatNews
	}
	return ""
}

// DedupKey builds a sha256 fingerprint over the supplied parts. Stable
// regardless of insertion order — but the caller is responsible for
// composing the parts in a deterministic order.
func DedupKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte("|"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
