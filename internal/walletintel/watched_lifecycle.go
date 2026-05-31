package walletintel

import (
	"context"
	"strings"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// handleLifecycle decides whether this trade is an exit on an existing
// open lifecycle row. Returns true if the trade was handled as an exit
// (and an exit alert routed); false means the caller should proceed with
// the normal NEW BET routing + open-lifecycle creation.
//
// Only watched wallets are processed (caller iterates the active watchlist).
// Direction truth comes from Data API (outcome+side), never WS.
func (w *WatchedWalletWorker) handleLifecycle(
	ctx context.Context,
	ww postgres.WatchedWallet,
	tradeID string,
	a dataapi.Activity,
	outcome, side string,
	price, size, notional float64,
	marketID, marketSlug, marketTitle, eventSlug string,
) bool {
	upSide := strings.ToUpper(side)
	if upSide != "SELL" && upSide != "BUY" {
		return false
	}

	lc, found, err := w.Store.FindOpenLifecycle(ctx, ww.ID, a.ConditionID, outcome)
	if err != nil || !found {
		return false
	}
	dirOpen := Direction(lc.OpenedDirection)
	if !IsExitSide(dirOpen, side) {
		return false
	}

	tolerance := w.ExitFullCloseTolerance
	if tolerance <= 0 {
		tolerance = 0.05
	}

	pnlEst, pnlKnown := ExitPnL(dirOpen, lc.OpenPrice, price, size)
	exitID, newStatus, inserted, err := w.Store.RecordExitAndUpdateLifecycle(ctx, postgres.ExitRecord{
		LifecycleID:     lc.ID,
		WalletTradeID:   tradeID,
		TransactionHash: a.TransactionHash,
		Side:            upSide,
		Price:           price,
		Size:            size,
		Notional:        notional,
		PnLEstimate:     pnlEst,
		PnLKnown:        pnlKnown,
		DetectedAt:      time.Unix(a.Timestamp.Int64(), 0),
	}, tolerance)
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("record exit", "err", err)
		}
		return true // treat as handled to avoid double-alerting via NEW BET path
	}
	if !inserted {
		// duplicate exit tx → no new alert
		return true
	}
	metrics.Inc("lifecycle_exits_total")
	if w.Log != nil {
		var pnlV any = "n/a"
		if pnlKnown {
			pnlV = round2For(pnlEst)
		}
		w.Log.Info("position exit detected",
			"wallet", ww.ProxyWallet,
			"market_id", lc.MarketID,
			"outcome", outcome,
			"status", newStatus,
			"exit_notional", round2For(notional),
			"avg_exit_price", round2For(price),
			"pnl_estimate", pnlV,
			"tx_hash", a.TransactionHash)
	}

	if !w.ExitAlertsEnabled {
		return true
	}

	severity := "INFO"
	if newStatus == "closed" {
		severity = "INFO"
	}
	held := ""
	if !lc.OpenedAt.IsZero() {
		d := time.Unix(a.Timestamp.Int64(), 0).Sub(lc.OpenedAt)
		if d > 0 {
			held = humanizeDuration(d)
		}
	}
	var missing []string
	if !pnlKnown {
		missing = append(missing, "MISSING_PNL_DATA")
	}

	payload := alerts.ExitAlert{
		WalletShort:   ww.ProxyWallet,
		WalletFull:    ww.ProxyWallet,
		Pseudonym:     ww.Pseudonym,
		ProfileSlug:   ww.ProfileSlug,
		Class:         ww.Class,
		MarketSlug:    marketSlug,
		MarketTitle:   marketTitle,
		EventSlug:     eventSlug,
		Outcome:       outcome,
		OpenedDir:     alerts.Direction(dirOpen),
		EntryPrice:    lc.OpenPrice,
		EntryNotional: lc.OpenNotional,
		ExitPrice:     price,
		ExitNotional:  notional,
		Status:        newStatus,
		PnLEstimate:   pnlEst,
		PnLKnown:      pnlKnown,
		HeldDuration:  held,
		Severity:      severity,
		MissingData:   missing,
	}
	body := alerts.FormatExitAlert(payload, w.Links)

	dedup := alerts.DedupKey("POSITION_EXIT", lc.ID, a.TransactionHash)
	decision := postgres.AlertDecision{
		AlertType:         alerts.TypePositionExit,
		EntityType:        "lifecycle_exit",
		EntityID:          exitID,
		Severity:          severity,
		ShouldSend:        true,
		UserAlertAllowed:  true,
		AdminAlertAllowed: true,
		ReasonCodes:       []string{"POSITION_EXIT", "STATUS_" + strings.ToUpper(newStatus)},
		MissingData:       missing,
		FeatureSnapshot: map[string]any{
			"lifecycle_id":  lc.ID,
			"opened_dir":    string(dirOpen),
			"entry_price":   lc.OpenPrice,
			"exit_price":    price,
			"exit_size":     size,
			"exit_notional": notional,
			"pnl_known":     pnlKnown,
			"pnl_estimate":  pnlEst,
			"status":        newStatus,
			"wallet_class":  ww.Class,
		},
		DedupKey: dedup,
	}
	out := w.Router.Route(ctx, decision, body, alerts.ChannelBets)
	if out.Err != nil && w.Log != nil {
		w.Log.Warn("position-exit alert send", "err", out.Err)
	}
	if out.Sent {
		metrics.Inc("alerts_position_exit_sent_total")
	}
	return true
}

func round2For(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

func humanizeDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	h := int(d.Hours())
	m := int(d.Minutes()) - h*60
	switch {
	case h >= 24:
		return formatDays(d)
	case h > 0:
		return fmtTwo(h, "h", m, "m")
	default:
		return fmtTwo(m, "m", 0, "")
	}
}

func formatDays(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) - days*24
	return fmtTwo(days, "d", hours, "h")
}

func fmtTwo(a int, au string, b int, bu string) string {
	if b == 0 || bu == "" {
		return numStr(a) + au
	}
	return numStr(a) + au + " " + numStr(b) + bu
}

func numStr(n int) string {
	if n == 0 {
		return "0"
	}
	// avoid fmt to keep allocation cheap (this runs per-trade)
	out := ""
	if n < 0 {
		out = "-"
		n = -n
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+(n%10))) + digits
		n /= 10
	}
	return out + digits
}
