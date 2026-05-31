package walletintel

import (
	"strings"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// ReconstructRealizedTrades walks a wallet's trades in chronological order
// and produces realized trade cycles per (condition_id, outcome). The
// algorithm uses weighted-average entry cost and FIFO-style realization on
// SELL — a BUY raises the open size and re-averages the entry cost; a SELL
// realizes PnL against the current avg entry on min(sell_size, open_size).
//
// IMPORTANT: PnL here is the TRADING profit (exit_price - avg_entry_price)
// multiplied by realized size. It is never derived from the market's final
// YES/NO resolution. A wallet that buys YES @ 0.20 and sells YES @ 0.45
// realizes a profit, even if the market later resolves NO.
//
// SELLs without a matching open size (size > open) realize what they can
// against the open portion and the remainder is dropped (we don't model
// shorts — that's an explicit Polymarket primitive the project doesn't
// support yet, and inventing short logic would distort PnL).
func ReconstructRealizedTrades(walletID string, trades []postgres.TradeForReplay) []postgres.RealizedTradeRow {
	type bookKey struct{ ConditionID, Outcome string }
	type book struct {
		openSize      float64
		avgEntryPrice float64
		entrySide     string
		entryTxHash   string
		entryTime     time.Time
		entryNotional float64
		marketID      string
		// outcome captured for the cycle even if outcome label varies
		outcome string
	}
	books := map[bookKey]*book{}
	var rows []postgres.RealizedTradeRow

	for _, t := range trades {
		side := strings.ToUpper(strings.TrimSpace(t.Side))
		outcome := strings.ToUpper(strings.TrimSpace(t.Outcome))
		if t.ConditionID == "" || outcome == "" || (side != "BUY" && side != "SELL") {
			continue
		}
		key := bookKey{ConditionID: t.ConditionID, Outcome: outcome}
		b := books[key]
		if b == nil {
			b = &book{outcome: outcome}
			books[key] = b
		}
		size := t.Size
		if size <= 0 {
			// Fall back to usdc_size/price reconstruction when share size missing.
			if t.Price > 0 && t.UsdcSize > 0 {
				size = t.UsdcSize / t.Price
			}
		}
		if size <= 0 {
			continue
		}
		price := t.Price
		if price <= 0 {
			continue
		}
		notional := t.UsdcSize
		if notional <= 0 {
			notional = size * price
		}

		switch side {
		case "BUY":
			// Increase open size, recompute weighted average entry price.
			newOpen := b.openSize + size
			if newOpen > 0 {
				b.avgEntryPrice = (b.avgEntryPrice*b.openSize + price*size) / newOpen
			}
			if b.openSize <= 0 {
				// fresh open — record the first entry hash + time
				b.entrySide = side
				b.entryTxHash = t.TransactionHash
				b.entryTime = t.Timestamp
				if t.MarketID != "" {
					b.marketID = t.MarketID
				}
			}
			b.openSize = newOpen
			b.entryNotional += notional
		case "SELL":
			if b.openSize <= 0 {
				// No open inventory — skip (we don't model shorts).
				continue
			}
			realized := size
			if realized > b.openSize {
				realized = b.openSize
			}
			realizedEntryNotional := b.avgEntryPrice * realized
			realizedExitNotional := price * realized
			pnl := realizedExitNotional - realizedEntryNotional
			roi := 0.0
			if realizedEntryNotional > 0 {
				roi = pnl / realizedEntryNotional
			}
			var entryT, exitT *time.Time
			if !b.entryTime.IsZero() {
				et := b.entryTime
				entryT = &et
			}
			if !t.Timestamp.IsZero() {
				xt := t.Timestamp
				exitT = &xt
			}
			var holdSec *int64
			if entryT != nil && exitT != nil {
				h := int64(exitT.Sub(*entryT).Seconds())
				if h < 0 {
					h = 0
				}
				holdSec = &h
			}
			rows = append(rows, postgres.RealizedTradeRow{
				WalletID:             walletID,
				MarketID:             b.marketID,
				ConditionID:          t.ConditionID,
				Outcome:              outcome,
				EntrySide:            "BUY",
				ExitSide:             side,
				EntryTransactionHash: b.entryTxHash,
				ExitTransactionHash:  t.TransactionHash,
				EntryTime:            entryT,
				ExitTime:             exitT,
				AvgEntryPrice:        b.avgEntryPrice,
				AvgExitPrice:         price,
				Size:                 realized,
				EntryNotional:        realizedEntryNotional,
				ExitNotional:         realizedExitNotional,
				RealizedPnL:          pnl,
				RealizedROI:          roi,
				HoldingSeconds:       holdSec,
				Source:               "reconstructed_trades",
				DataQuality:          "complete",
				Raw: postgres.RawJSON(map[string]any{
					"sell_size_requested": size,
					"open_before":         b.openSize,
				}),
			})
			b.openSize -= realized
			// proportional reduction of accumulated entry notional so future
			// partial exits don't double-count it
			if b.entryNotional > 0 {
				ratio := realized / (realized + b.openSize)
				b.entryNotional -= b.entryNotional * ratio
				if b.entryNotional < 0 {
					b.entryNotional = 0
				}
			}
		}
	}
	return rows
}
