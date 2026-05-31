package walletintel

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// ClusterWorker scans recent watched bets and emits cluster decisions.
// Operates ONLY on watchlisted wallets (postgres.ListRecentWatchedBets is
// already populated only from watched wallets).
type ClusterWorker struct {
	Store    *postgres.Store
	Router   *alerts.Router
	Log      *slog.Logger
	Interval time.Duration
	Params   ClusterParams
	Links    alerts.LinkBuilder

	// IncludeExits controls whether SELL trades enter many-trader cluster
	// detection. Mirrors EXIT_CLUSTER_ENABLED config (default false).
	IncludeExits bool

	// Cluster profit gate: a cluster must also satisfy total profit-if-win
	// OR (total notional >= min AND avg odds >= min) to emit an alert.
	// Zero values disable the respective check.
	ClusterMinTotalProfit float64
	ClusterMinAvgOdds     float64
}

func (w *ClusterWorker) Run(ctx context.Context) error {
	if w.Interval <= 0 {
		w.Interval = 90 * time.Second
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

func (w *ClusterWorker) runOnce(ctx context.Context) {
	start := time.Now()
	since := time.Now().Add(-w.Params.WindowBefore - w.Params.WindowAfter - 1*time.Hour)
	recent, err := w.Store.ListRecentWatchedBets(ctx, since, w.IncludeExits)
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("cluster list watched bets", "err", err)
		}
		return
	}
	var clustersDetected, alertsCreated int
	bets := make([]WatchedBet, 0, len(recent))
	for _, r := range recent {
		bets = append(bets, WatchedBet{
			ID:          r.ID,
			WalletID:    r.WalletID,
			WalletClass: r.WalletClass,
			WalletScore: r.WalletScore,
			MarketID:    r.MarketID,
			EventID:     "",
			MarketSlug:  r.MarketSlug,
			EventSlug:   r.EventSlug,
			MarketTitle: r.MarketTitle,
			EventTitle:  r.EventTitle,
			Direction:   Direction(r.Direction),
			Outcome:     r.DirectionOutcome, // used as cluster group key for categorical
			Notional:    r.Notional,
			Price:       r.Price,
			Timestamp:   r.DetectedAt,
		})
	}
	results := FindClusters(bets, w.Params)
	for _, c := range results {
		alertSent := w.persistAndRoute(ctx, c)
		clustersDetected++
		if alertSent {
			alertsCreated++
		}
	}
	if w.Log != nil {
		w.Log.Info("cluster cycle completed",
			"recent_bets", len(recent),
			"clusters_detected", clustersDetected,
			"alerts_created", alertsCreated,
			"duration", time.Since(start).String())
	}
}

func (w *ClusterWorker) persistAndRoute(ctx context.Context, c ClusterResult) bool {
	// Cluster profit gate: check total profit-if-win and avg odds thresholds.
	// totalProfit = PayoffIfWinTotal - TotalNotional
	if w.ClusterMinTotalProfit > 0 || w.ClusterMinAvgOdds > 0 {
		totalProfit := c.PayoffIfWinTotal - c.TotalNotional
		profitOK := w.ClusterMinTotalProfit <= 0 || totalProfit >= w.ClusterMinTotalProfit
		oddsOK := w.ClusterMinAvgOdds <= 0 || c.AverageOdds >= w.ClusterMinAvgOdds
		notionalOK := c.TotalNotional >= w.Params.MinTotalNotional
		// Pass if: profit gate passes OR (notional gate passes AND odds gate passes)
		gatePass := profitOK || (notionalOK && oddsOK)
		if !gatePass {
			if w.Log != nil {
				w.Log.Info("cluster suppressed by profit gate",
					"total_notional", c.TotalNotional,
					"total_profit_if_win", totalProfit,
					"avg_odds", c.AverageOdds,
					"min_total_profit", w.ClusterMinTotalProfit,
					"min_avg_odds", w.ClusterMinAvgOdds)
			}
			metrics.Inc("cluster_alerts_suppressed_profit_gate_total")
			return false
		}
	}

	_, inserted, err := w.Store.UpsertCluster(ctx, ToClusterRow(c))
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("upsert cluster", "err", err)
		}
		return false
	}
	metrics.Inc("clusters_seen_total")
	if !inserted {
		return false // already existed; skip alert
	}
	metrics.Inc("clusters_new_total")
	if w.Log != nil {
		w.Log.Info("cluster detected",
			"market_id", c.MarketID,
			"direction", string(c.Direction),
			"direction_outcome", c.DirectionOutcome,
			"wallets", c.WalletCount,
			"total_notional", c.TotalNotional,
			"cluster_score", c.ClusterScore)
	}

	// Build trader list (wallet display names)
	var traders []alerts.ClusterTrader
	if len(c.WalletIDs) > 0 {
		rows, err := w.Store.Pool.Query(ctx, `
			SELECT w.proxy_wallet, COALESCE(w.pseudonym,''), COALESCE(w.profile_slug,''),
			       COALESCE(ww.class,''), COALESCE(ww.score,0)
			FROM wallets w
			LEFT JOIN wallet_watchlist ww ON ww.wallet_id = w.id
			WHERE w.id::text = ANY($1)
		`, c.WalletIDs)
		if err == nil {
			for rows.Next() {
				var proxy, pseu, slug, class string
				var score int
				if err := rows.Scan(&proxy, &pseu, &slug, &class, &score); err == nil {
					traders = append(traders, alerts.ClusterTrader{
						WalletShort: proxy,
						WalletFull:  proxy,
						Pseudonym:   pseu,
						ProfileSlug: slug,
						Class:       class,
						Score:       score,
					})
				}
			}
			rows.Close()
		}
	}

	winSecs := int(c.WindowEnd.Sub(c.WindowStart).Seconds())
	body := alerts.FormatClusterAlert(alerts.ClusterAlert{
		Direction:        alerts.Direction(c.Direction),
		DirectionOutcome: c.DirectionOutcome,
		MarketSlug:       c.MarketSlug,
		MarketTitle:      firstNonEmpty(c.MarketTitle, c.MarketSlug),
		EventSlug:        c.EventSlug,
		EventTitle:       c.EventTitle,
		TotalNotional:    c.TotalNotional,
		WalletCount:      c.WalletCount,
		WeightedPrice:    c.WeightedPrice,
		AverageOdds:      c.AverageOdds,
		PayoffIfWin:      c.PayoffIfWinTotal,
		WindowSeconds:    winSecs,
		Traders:          traders,
		ReasonCodes:      c.ReasonCodes,
	}, w.Links)

	decision := postgres.AlertDecision{
		AlertType:         alerts.TypeClusterBet,
		EntityType:        "bet_cluster",
		EntityID:          c.DedupKey,
		Severity:          "HIGH",
		ShouldSend:        true,
		UserAlertAllowed:  true,
		AdminAlertAllowed: true,
		ReasonCodes:       c.ReasonCodes,
		FeatureSnapshot:   map[string]any(c.FeatureSnapshot),
		DedupKey:          alerts.DedupKey("CLUSTER_BET", c.DedupKey),
	}
	out := w.Router.Route(ctx, decision, body, alerts.ChannelClusters)
	if out.Err != nil && w.Log != nil {
		w.Log.Warn("cluster alert", "err", out.Err)
	}
	if out.Sent {
		metrics.Inc("alerts_cluster_sent_total")
	}
	return out.Sent || out.DecisionNew
}

func firstNonEmpty(xs ...string) string {
	for _, s := range xs {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
