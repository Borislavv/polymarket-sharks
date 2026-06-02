package postgres

import (
	"context"
	"encoding/json"
	"time"
)

// RealizedTradeRow is one closed trading cycle: one or more BUYs followed by
// a SELL that reduced or closed the wallet's position on (condition, outcome).
// PnL here is reconstructed from BUY/SELL price differences — it is the real
// trading profit, never inferred from the market's final YES/NO outcome.
type RealizedTradeRow struct {
	WalletID             string
	MarketID             string // empty when unknown
	ConditionID          string
	Outcome              string
	EntrySide            string
	ExitSide             string
	EntryTransactionHash string
	ExitTransactionHash  string
	EntryTime            *time.Time
	ExitTime             *time.Time
	AvgEntryPrice        float64
	AvgExitPrice         float64
	Size                 float64
	EntryNotional        float64
	ExitNotional         float64
	RealizedPnL          float64
	RealizedROI          float64
	HoldingSeconds       *int64
	Source               string // reconstructed_trades | api_realized_pnl | resolution_payout
	DataQuality          string // complete | partial | proxy
	Raw                  []byte
}

// InsertRealizedTrades persists a batch of reconstructed cycles in one txn.
// Returns count of newly-inserted rows (duplicates are silently skipped).
func (s *Store) InsertRealizedTrades(ctx context.Context, rows []RealizedTradeRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	const q = `
		INSERT INTO wallet_realized_trades
		    (wallet_id, market_id, condition_id, outcome,
		     entry_side, exit_side, entry_transaction_hash, exit_transaction_hash,
		     entry_time, exit_time,
		     avg_entry_price, avg_exit_price, size,
		     entry_notional, exit_notional,
		     realized_pnl, realized_roi, holding_seconds,
		     source, data_quality, raw)
		VALUES
		    ($1::uuid, NULLIF($2,'')::uuid, $3, $4,
		     $5, $6, $7, $8,
		     $9, $10,
		     $11, $12, $13,
		     $14, $15,
		     $16, $17, $18,
		     $19, $20, $21::jsonb)
		ON CONFLICT (wallet_id, condition_id, outcome, exit_transaction_hash, size) DO NOTHING`
	inserted := 0
	for _, r := range rows {
		raw := r.Raw
		if len(raw) == 0 {
			raw = []byte(`{}`)
		}
		ct, err := tx.Exec(ctx, q,
			r.WalletID, r.MarketID, r.ConditionID, r.Outcome,
			r.EntrySide, r.ExitSide, r.EntryTransactionHash, r.ExitTransactionHash,
			r.EntryTime, r.ExitTime,
			r.AvgEntryPrice, r.AvgExitPrice, r.Size,
			r.EntryNotional, r.ExitNotional,
			r.RealizedPnL, r.RealizedROI, r.HoldingSeconds,
			r.Source, r.DataQuality, string(raw),
		)
		if err != nil {
			return inserted, err
		}
		inserted += int(ct.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return inserted, err
	}
	return inserted, nil
}

// RealizedStats is the aggregate read by v4 shark/insider scoring. Computed
// in pure SQL over the latest realized-trade snapshot per wallet.
type RealizedStats struct {
	Cycles                 int
	ProfitableCycles       int
	LosingCycles           int
	BreakevenCycles        int
	TotalRealizedPnL       float64
	GrossProfit            float64
	GrossLoss              float64
	AvgRealizedNotional    float64
	MedianRealizedNotional float64
	TotalRealizedVolume    float64
	AvgRealizedROI         float64
	ProfitFactor           float64 // gross_profit / |gross_loss|; 0 when no losses
	MaxRealizedWin         float64
	MaxRealizedLoss        float64
	AvgWinUSD              float64
	MedianWinUSD           float64
	AvgLossUSD             float64 // negative value
	MedianLossUSD          float64 // negative value
	FirstExitAt            *time.Time
	LastExitAt             *time.Time
	LastProfitableExitAt   *time.Time
	WinRate                float64 // == profitable_exit_rate
}

// TradeWindowStats summarises trading activity in a rolling window.
type TradeWindowStats struct {
	Trades     int
	FirstTrade *time.Time
	LastTrade  *time.Time
}

// RealizedWindowStats summarises realized-cycle performance in a rolling window.
type RealizedWindowStats struct {
	Cycles          int
	TotalPnL        float64
	TotalEntryStake float64
}

// GetRealizedStats reads aggregate realized profitability for a wallet.
// Pure SQL — no per-row JSON parsing. Returns zero-value when no rows yet.
func (s *Store) GetRealizedStats(ctx context.Context, walletID string) (RealizedStats, error) {
	const q = `
		SELECT
		    COALESCE(count(*),0)                                              AS cycles,
		    COALESCE(count(*) FILTER (WHERE realized_pnl > 0),0)              AS profitable,
		    COALESCE(count(*) FILTER (WHERE realized_pnl < 0),0)              AS losing,
		    COALESCE(count(*) FILTER (WHERE realized_pnl = 0),0)              AS breakeven,
		    COALESCE(sum(realized_pnl),0)                                     AS total_pnl,
		    COALESCE(sum(realized_pnl) FILTER (WHERE realized_pnl > 0),0)     AS gross_profit,
		    COALESCE(sum(realized_pnl) FILTER (WHERE realized_pnl < 0),0)     AS gross_loss,
		    COALESCE(avg(entry_notional),0)                                   AS avg_notional,
		    COALESCE(sum(entry_notional),0)                                   AS total_volume,
		    COALESCE(avg(realized_roi),0)                                     AS avg_roi,
		    COALESCE(max(realized_pnl) FILTER (WHERE realized_pnl > 0),0)     AS max_win,
		    COALESCE(min(realized_pnl) FILTER (WHERE realized_pnl < 0),0)     AS max_loss,
		    COALESCE(avg(realized_pnl) FILTER (WHERE realized_pnl > 0),0)     AS avg_win,
		    COALESCE(avg(realized_pnl) FILTER (WHERE realized_pnl < 0),0)     AS avg_loss,
		    min(exit_time)                                                    AS first_exit,
		    max(exit_time)                                                    AS last_exit,
		    max(exit_time) FILTER (WHERE realized_pnl > 0)                    AS last_profitable_exit
		FROM wallet_realized_trades
		WHERE wallet_id = $1::uuid`
	var (
		st                                  RealizedStats
		firstExit, lastExit, lastProfitable *time.Time
	)
	err := s.Pool.QueryRow(ctx, q, walletID).Scan(
		&st.Cycles, &st.ProfitableCycles, &st.LosingCycles, &st.BreakevenCycles,
		&st.TotalRealizedPnL, &st.GrossProfit, &st.GrossLoss,
		&st.AvgRealizedNotional, &st.TotalRealizedVolume, &st.AvgRealizedROI,
		&st.MaxRealizedWin, &st.MaxRealizedLoss,
		&st.AvgWinUSD, &st.AvgLossUSD,
		&firstExit, &lastExit, &lastProfitable,
	)
	if err != nil {
		return st, err
	}
	st.FirstExitAt = firstExit
	st.LastExitAt = lastExit
	st.LastProfitableExitAt = lastProfitable
	if st.Cycles > 0 {
		st.WinRate = float64(st.ProfitableCycles) / float64(st.Cycles)
	}
	if st.GrossLoss < 0 {
		st.ProfitFactor = st.GrossProfit / (-st.GrossLoss)
	}
	// Medians via separate cheap queries — `percentile_cont` doesn't accept
	// FILTER and we already gated by wallet_id so this stays O(N) once.
	if st.Cycles > 1 {
		const qmedian = `
			SELECT
			    percentile_cont(0.5) WITHIN GROUP (ORDER BY entry_notional) FILTER (WHERE entry_notional > 0),
			    percentile_cont(0.5) WITHIN GROUP (ORDER BY realized_pnl)   FILTER (WHERE realized_pnl > 0),
			    percentile_cont(0.5) WITHIN GROUP (ORDER BY realized_pnl)   FILTER (WHERE realized_pnl < 0)
			FROM wallet_realized_trades WHERE wallet_id = $1::uuid`
		var medN, medWin, medLoss *float64
		if err := s.Pool.QueryRow(ctx, qmedian, walletID).Scan(&medN, &medWin, &medLoss); err == nil {
			if medN != nil {
				st.MedianRealizedNotional = *medN
			}
			if medWin != nil {
				st.MedianWinUSD = *medWin
			}
			if medLoss != nil {
				st.MedianLossUSD = *medLoss
			}
		}
	} else if st.Cycles == 1 {
		st.MedianRealizedNotional = st.AvgRealizedNotional
		st.MedianWinUSD = st.AvgWinUSD
		st.MedianLossUSD = st.AvgLossUSD
	}
	return st, nil
}

// GetTradeWindowStats returns trade activity for [since, now].
func (s *Store) GetTradeWindowStats(ctx context.Context, walletID string, since time.Time) (TradeWindowStats, error) {
	const q = `
		SELECT
		    COALESCE(count(*),0) AS trades,
		    min(timestamp)       AS first_trade,
		    max(timestamp)       AS last_trade
		FROM wallet_trades
		WHERE wallet_id = $1::uuid
		  AND timestamp >= $2`
	var (
		st          TradeWindowStats
		first, last *time.Time
	)
	err := s.Pool.QueryRow(ctx, q, walletID, since).Scan(&st.Trades, &first, &last)
	if err != nil {
		return st, err
	}
	st.FirstTrade = first
	st.LastTrade = last
	return st, nil
}

// GetRealizedWindowStats returns realized-cycle profitability for [since, now].
// Profit percentage is computed by callers as TotalPnL / TotalEntryStake.
func (s *Store) GetRealizedWindowStats(ctx context.Context, walletID string, since time.Time) (RealizedWindowStats, error) {
	const q = `
		SELECT
		    COALESCE(count(*),0)        AS cycles,
		    COALESCE(sum(realized_pnl),0),
		    COALESCE(sum(entry_notional),0)
		FROM wallet_realized_trades
		WHERE wallet_id = $1::uuid
		  AND exit_time IS NOT NULL
		  AND exit_time >= $2`
	var st RealizedWindowStats
	err := s.Pool.QueryRow(ctx, q, walletID, since).Scan(&st.Cycles, &st.TotalPnL, &st.TotalEntryStake)
	return st, err
}

// DiscoverySampleStats summarises the breadth of evidence behind a SHARK or
// INSIDER discovery decision: how many trades were considered, how many
// closed positions were observed, and how many positions remain open and
// therefore are NOT counted toward success rate.
type DiscoverySampleStats struct {
	TradesChecked       int
	PositionsChecked    int
	OpenUnresolvedCount int
}

// GetDiscoverySampleStats reads the breadth of evidence in pure SQL.
func (s *Store) GetDiscoverySampleStats(ctx context.Context, walletID string) (DiscoverySampleStats, error) {
	const q = `
		SELECT
		    (SELECT count(*) FROM wallet_trades WHERE wallet_id = $1::uuid)                                      AS trades_checked,
		    (SELECT count(*) FROM wallet_closed_position_latest WHERE wallet_id = $1::uuid)                       AS positions_checked,
		    (SELECT count(*) FROM wallet_closed_position_latest WHERE wallet_id = $1::uuid AND is_closed = false) AS open_unresolved
	`
	var st DiscoverySampleStats
	err := s.Pool.QueryRow(ctx, q, walletID).Scan(&st.TradesChecked, &st.PositionsChecked, &st.OpenUnresolvedCount)
	return st, err
}

// LatestTradesForWallet returns the wallet's stored trades sorted by time
// ascending — input for realized-cycle reconstruction.
func (s *Store) LatestTradesForWallet(ctx context.Context, walletID string, limit int) ([]TradeForReplay, error) {
	if limit <= 0 {
		limit = 10000
	}
	const q = `
		SELECT id::text, transaction_hash, COALESCE(market_id::text,''),
		       COALESCE(condition_id,''), COALESCE(outcome,''),
		       COALESCE(side,''), COALESCE(direction,''),
		       COALESCE(price,0), COALESCE(size,0), COALESCE(usdc_size,0),
		       timestamp
		FROM wallet_trades
		WHERE wallet_id = $1::uuid
		ORDER BY timestamp ASC
		LIMIT $2`
	rows, err := s.Pool.Query(ctx, q, walletID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TradeForReplay
	for rows.Next() {
		var t TradeForReplay
		if err := rows.Scan(&t.ID, &t.TransactionHash, &t.MarketID,
			&t.ConditionID, &t.Outcome, &t.Side, &t.Direction,
			&t.Price, &t.Size, &t.UsdcSize, &t.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TradeForReplay is the lean read shape consumed by realized-cycle
// reconstruction. Mirrors wallet_trades schema.
type TradeForReplay struct {
	ID              string
	TransactionHash string
	MarketID        string
	ConditionID     string
	Outcome         string
	Side            string
	Direction       string
	Price           float64
	Size            float64
	UsdcSize        float64
	Timestamp       time.Time
}

// rawMessage helper used by callers to embed wallet+market context into the
// realized cycle's `raw` jsonb without exposing the json package outside.
func RawJSON(v any) []byte {
	b, _ := json.Marshal(v)
	if len(b) == 0 {
		b = []byte(`{}`)
	}
	return b
}
