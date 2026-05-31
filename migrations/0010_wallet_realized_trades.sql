-- migration 0010: reconstructed realized trade cycles.
--
-- A realized trade cycle is one closed slice of a wallet's position on
-- (condition_id, outcome): one or more BUYs followed by SELLs that reduce
-- the position. The realized PnL is the per-share profit captured by the
-- exit price vs. the weighted-average entry price, NOT the final market
-- outcome — a wallet that buys YES @ 0.20 and sells YES @ 0.45 has a real
-- realized profit, even if the market later resolves NO.
--
-- This table is the truth source for `realized_win_rate` /
-- `profitable_exit_rate` / `realized_roi` used by v4 shark scoring and
-- insider-like first-win confirmation.

CREATE TABLE IF NOT EXISTS wallet_realized_trades (
    id                       bigserial PRIMARY KEY,
    wallet_id                uuid NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    market_id                uuid REFERENCES markets(id) ON DELETE SET NULL,
    condition_id             text,
    outcome                  text,
    entry_side               text,         -- BUY for long; SELL for short (not currently supported)
    exit_side                text,         -- the side of the closing trade
    entry_transaction_hash   text,
    exit_transaction_hash    text,
    entry_time               timestamptz,
    exit_time                timestamptz,
    avg_entry_price          numeric NOT NULL,
    avg_exit_price           numeric NOT NULL,
    size                     numeric NOT NULL,         -- realized share size
    entry_notional           numeric NOT NULL,
    exit_notional            numeric NOT NULL,
    realized_pnl             numeric NOT NULL,
    realized_roi             numeric NOT NULL,
    holding_seconds          bigint,
    source                   text NOT NULL,            -- reconstructed_trades | api_realized_pnl | resolution_payout
    data_quality             text NOT NULL,            -- complete | partial | proxy
    raw                      jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at               timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_wrt_wallet_time
    ON wallet_realized_trades(wallet_id, exit_time DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_wrt_wallet_market_outcome
    ON wallet_realized_trades(wallet_id, condition_id, outcome);

-- Idempotency: re-running reconstruction on the same exit trade must not
-- create duplicates. Multiple exits on the same position share the exit hash
-- only when partial; we include size to disambiguate split exits within a
-- single transaction (rare but possible).
CREATE UNIQUE INDEX IF NOT EXISTS uq_wrt_cycle
    ON wallet_realized_trades(wallet_id, condition_id, outcome, exit_transaction_hash, size);
