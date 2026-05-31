package marketscan

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// HolderScanWorker periodically polls /holders for hotset markets, persists
// snapshots with rank, and enqueues new/interesting wallets for scoring.
// Deep-scan via /v1/market-positions is bounded to top-N markets.
type HolderScanWorker struct {
	DataAPI      *dataapi.Client
	Store        *postgres.Store
	Log          *slog.Logger
	Interval     time.Duration
	HotsetSize   int
	DeepScanSize int
	OnNewWallet  func(walletID, proxy string)
}

func (w *HolderScanWorker) Run(ctx context.Context) error {
	if w.Interval <= 0 {
		w.Interval = 10 * time.Minute
	}
	if w.HotsetSize <= 0 {
		w.HotsetSize = 80
	}
	if w.DeepScanSize <= 0 {
		// v4: default 50 (was 10), aligned with HOLDER_DEEP_SCAN_SIZE.
		w.DeepScanSize = 50
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

func (w *HolderScanWorker) runOnce(ctx context.Context) {
	start := time.Now()
	mkts, err := w.Store.ListHotsetCandidates(ctx, w.HotsetSize)
	if err != nil {
		return
	}
	var holdersFetched, snapshotsInserted, newWallets, scoringTriggered int
	for i, m := range mkts {
		holders, _, err := w.DataAPI.GetHoldersByMarket(ctx, m.ConditionID)
		if err != nil {
			metrics.Inc("holders_errors_total")
			continue
		}
		holdersFetched += len(holders)
		// rank by amount desc per outcome
		grouped := map[int][]dataapi.FlatHolder{}
		for _, h := range holders {
			grouped[h.OutcomeIndex] = append(grouped[h.OutcomeIndex], h)
		}
		now := time.Now()
		for outcome, hs := range grouped {
			sort.Slice(hs, func(i, j int) bool { return hs[i].Amount > hs[j].Amount })
			for rank, h := range hs {
				walletID, isNew, err := w.Store.UpsertWalletReturn(ctx, postgres.Wallet{
					ProxyWallet: h.ProxyWallet,
					Pseudonym:   h.Pseudonym,
				})
				if err != nil {
					continue
				}
				raw, _ := json.Marshal(h)
				if err := w.Store.InsertHolderSnapshot(ctx, postgres.HolderSnapshot{
					MarketID:           m.ID,
					WalletID:           walletID,
					OutcomeIndex:       outcome,
					Amount:             h.Amount,
					Rank:               rank + 1,
					PctOutcomeSnapshot: 0,
					PctValid:           false,
					Source:             "data-api/holders",
					Raw:                raw,
					SnapshotAt:         now,
				}); err == nil {
					snapshotsInserted++
				}
				if rank < 5 {
					scoringTriggered++
					if w.Log != nil {
						msg := "wallet observed in market"
						if isNew {
							msg = "new wallet discovered"
							newWallets++
						}
						w.Log.Info(msg,
							"wallet", h.ProxyWallet,
							"market_id", m.ID,
							"outcome", outcome,
							"amount", h.Amount,
							"rank", rank+1,
							"is_new", isNew)
					}
					if w.OnNewWallet != nil {
						w.OnNewWallet(walletID, h.ProxyWallet)
					}
				}
				metrics.Inc("holders_snapshots_total")
			}
		}
		// deep scan for top markets only
		if i < w.DeepScanSize {
			w.deepScan(ctx, m)
		}
	}
	if w.Log != nil {
		w.Log.Info("holder scan completed",
			"markets_scanned", len(mkts),
			"holders_fetched", holdersFetched,
			"snapshots_inserted", snapshotsInserted,
			"new_wallets", newWallets,
			"scoring_triggered", scoringTriggered,
			"duration", time.Since(start).String())
	}
}

func (w *HolderScanWorker) deepScan(ctx context.Context, m postgres.MarketSummary) {
	// Top-20 by volume (TOKENS) AND top-20 by CASH_PNL → dedup → candidate evidence.
	const topN = 20
	byVol, _, err := w.DataAPI.GetMarketPositionsSorted(ctx, m.ConditionID, dataapi.SortByTokens, topN)
	if err != nil {
		metrics.Inc("positions_errors_total")
	}
	byPnL, _, err := w.DataAPI.GetMarketPositionsSorted(ctx, m.ConditionID, dataapi.SortByCashPnl, topN)
	if err != nil {
		metrics.Inc("positions_errors_total")
	}
	w.ingestPositionEvidence(ctx, m, byVol, postgres.EvidenceSourcePositionsVolume)
	w.ingestPositionEvidence(ctx, m, byPnL, postgres.EvidenceSourcePositionsPNL)

	// Legacy snapshot path (kept so holder_snapshots stays populated):
	positions, _, err := w.DataAPI.GetMarketPositions(ctx, m.ConditionID, 50)
	if err != nil {
		metrics.Inc("positions_errors_total")
		return
	}
	for _, p := range positions {
		walletID, err := w.Store.UpsertWallet(ctx, postgres.Wallet{ProxyWallet: p.ProxyWallet})
		if err != nil {
			continue
		}
		raw, _ := json.Marshal(p)
		_ = w.Store.InsertHolderSnapshot(ctx, postgres.HolderSnapshot{
			MarketID:     m.ID,
			WalletID:     walletID,
			OutcomeIndex: p.OutcomeIndex,
			Amount:       p.Size.Float64(),
			Rank:         0,
			PctValid:     false,
			Source:       "data-api/market-positions",
			Raw:          raw,
			SnapshotAt:   time.Now(),
		})
		metrics.Inc("positions_snapshots_total")
	}
}

// ingestPositionEvidence persists per-wallet candidate evidence rows from
// a sorted /v1/market-positions slice. Rank starts at 1.
func (w *HolderScanWorker) ingestPositionEvidence(ctx context.Context, m postgres.MarketSummary, positions []dataapi.Position, source string) {
	for rank, p := range positions {
		if p.ProxyWallet == "" {
			continue
		}
		walletID, err := w.Store.UpsertWallet(ctx, postgres.Wallet{ProxyWallet: p.ProxyWallet, Pseudonym: p.Title})
		if err != nil {
			continue
		}
		raw, _ := json.Marshal(p)
		snap := map[string]any{
			"avg_price":     p.AvgPrice.Float64(),
			"size":          p.Size.Float64(),
			"current_price": p.CurPrice.Float64(),
			"outcome":       p.Outcome,
		}
		_ = w.Store.InsertCandidateEvidence(ctx, postgres.CandidateEvidence{
			WalletID:     walletID,
			MarketID:     m.ID,
			Source:       source,
			SourceRank:   rank + 1,
			CurrentValue: p.CurrentValue.Float64(),
			TotalBought:  p.TotalBought.Float64(),
			CashPnL:      p.CashPnl.Float64(),
			RealizedPnL:  p.RealizedPnl.Float64(),
			PercentPnL:   p.PercentPnl.Float64(),
			Snapshot:     snap,
			Raw:          raw,
		})
		if w.OnNewWallet != nil && rank < 10 {
			w.OnNewWallet(walletID, p.ProxyWallet)
		}
		metrics.Inc("candidate_evidence_inserted_total")
	}
}
