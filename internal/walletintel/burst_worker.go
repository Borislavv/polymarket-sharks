package walletintel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// BurstWorker is the same-wallet aggregation worker. It scans entry-kind
// watched_bets over a rolling window (BurstWindow) and, for any watched
// wallet that breached BurstMinBets OR BurstMinDistinctMarkets, emits a
// single SHARK_BURST decision via AlertRouter — instead of N individual
// dust SHARK_BET alerts.
//
// Many-trader cluster logic stays separate (ClusterWorker); BurstWorker
// only ever aggregates within one wallet_id.
type BurstWorker struct {
	Store    *postgres.Store
	Router   *alerts.Router
	Log      *slog.Logger
	Links    alerts.LinkBuilder
	Interval time.Duration

	Window           time.Duration
	MinBets          int
	MinMarkets       int
	MinTotalNotional float64
}

func (w *BurstWorker) Run(ctx context.Context) error {
	if w.Interval <= 0 {
		w.Interval = 60 * time.Second
	}
	if w.Window <= 0 {
		w.Window = 15 * time.Minute
	}
	if w.MinBets <= 0 {
		w.MinBets = 3
	}
	if w.MinMarkets <= 0 {
		w.MinMarkets = 2
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

func (w *BurstWorker) runOnce(ctx context.Context) {
	start := time.Now()
	cands, err := w.Store.FindBurstCandidates(ctx, time.Now(), w.Window, w.MinBets, w.MinMarkets, ScoreVersion)
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("burst find", "err", err)
		}
		return
	}
	emitted := 0
	for _, c := range cands {
		if c.TotalNotional < w.MinTotalNotional {
			continue
		}
		if w.emitBurst(ctx, c) {
			emitted++
		}
	}
	if w.Log != nil {
		w.Log.Info("burst cycle completed",
			"candidates", len(cands),
			"alerts_created", emitted,
			"window", w.Window.String(),
			"duration", time.Since(start).String())
	}
}

func (w *BurstWorker) emitBurst(ctx context.Context, c postgres.BurstCandidate) bool {
	// dedup key = wallet_id + last_at rounded to BurstWindow bucket.
	bucket := c.LastAt.UTC().Truncate(w.Window).Format(time.RFC3339)
	dedupRaw := fmt.Sprintf("BURST|%s|%s", c.WalletID, bucket)
	h := sha256.Sum256([]byte(dedupRaw))
	dedup := hex.EncodeToString(h[:])

	markets := make([]alerts.BurstMarketLine, 0, len(c.Markets))
	for _, m := range c.Markets {
		markets = append(markets, alerts.BurstMarketLine{
			Slug:      m.MarketSlug,
			Title:     m.MarketTitle,
			Direction: alerts.Direction(m.Direction),
			Notional:  m.Notional,
			BetCount:  m.BetCount,
		})
	}

	body := alerts.FormatBurstAlert(alerts.BurstAlert{
		WalletShort:     c.ProxyWallet,
		WalletFull:      c.ProxyWallet,
		Pseudonym:       c.Pseudonym,
		ProfileSlug:     c.ProfileSlug,
		Class:           c.Class,
		BetsCount:       c.BetsCount,
		DistinctMarkets: c.DistinctMarkets,
		TotalNotional:   c.TotalNotional,
		WeightedPrice:   c.WeightedPrice,
		WindowMinutes:   int(w.Window.Minutes()),
		Markets:         markets,
	}, w.Links)

	decision := postgres.AlertDecision{
		AlertType:         alerts.TypeSharkBurst,
		EntityType:        "burst",
		EntityID:          c.WalletID,
		Severity:          "WARNING",
		ShouldSend:        true,
		UserAlertAllowed:  true,
		AdminAlertAllowed: true,
		ReasonCodes:       []string{"SAME_WALLET_BURST"},
		FeatureSnapshot: map[string]any{
			"wallet_id":        c.WalletID,
			"bets_count":       c.BetsCount,
			"distinct_markets": c.DistinctMarkets,
			"total_notional":   c.TotalNotional,
			"window_minutes":   int(w.Window.Minutes()),
			"class":            c.Class,
		},
		DedupKey: dedup,
	}
	out := w.Router.Route(ctx, decision, body, alerts.ChannelClusters)
	if out.Err != nil && w.Log != nil {
		w.Log.Warn("burst alert send", "err", out.Err)
	}
	if out.Sent {
		metrics.Inc("alerts_burst_sent_total")
		return true
	}
	if w.Log != nil && out.DecisionNew {
		w.Log.Info("burst alert created",
			"wallet", c.ProxyWallet,
			"bets", c.BetsCount,
			"markets", c.DistinctMarkets)
	}
	return out.DecisionNew
}
