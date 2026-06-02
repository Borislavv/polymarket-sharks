-- migration 0009: historical wallet data backfill for v4 shark scoring.
--
-- v4 shark strategy is based on the wallet's full historical /closed-positions
-- (not on open lifecycle inside our service, not on position.size==0 inferred
-- in-process). This migration introduces:
--   * wallet_closed_positions: append-only snapshot of every closed position
--     we have observed for a wallet via the Polymarket Data API. Source of
--     truth for ROI / win-rate / avg-stake / realized-PnL for v4 shark gates.
--   * wallet_history_backfills: per-wallet bookkeeping for the backfill
--     worker — counters, completeness flags, last-attempt timestamp.
--   * statuses 'needs_history' and 'streak_broken' for wallet_watchlist;
--     enforced at write-time by application code (kept as plain text for
--     forward compatibility — no CHECK constraint to allow new statuses).

CREATE TABLE IF NOT EXISTS wallet_closed_positions (
    id                  bigserial PRIMARY KEY,
    wallet_id           uuid NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    condition_id        text,
    market_id           uuid REFERENCES markets(id) ON DELETE SET NULL,
    event_slug          text,
    outcome             text,
    outcome_index       int,
    total_bought        numeric,
    realized_pnl        numeric,
    avg_price           numeric,
    current_value       numeric,
    percent_pnl         numeric,
    percent_realized_pnl numeric,
    -- size at observation; 0 indicates fully redeemed/closed, but the gate
    -- decision is encoded server-side via `is_closed` so the scoring layer
    -- never has to re-derive "closed" from size.
    size_at_observation numeric,
    is_closed           boolean NOT NULL,
    closed_at           timestamptz,
    observed_at         timestamptz NOT NULL DEFAULT now(),
    raw                 jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_wcp_wallet_time
    ON wallet_closed_positions(wallet_id, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_wcp_wallet_market
    ON wallet_closed_positions(wallet_id, condition_id, outcome);
-- Last-observed snapshot per (wallet, condition, outcome) is what scoring
-- reads; older rows are kept for audit.
--
-- NOTE: this index is intentionally non-unique to keep migration idempotent on
-- historical datasets that may already contain duplicate observed_at rows.
CREATE INDEX IF NOT EXISTS idx_wcp_wallet_position_observed
    ON wallet_closed_positions(wallet_id, condition_id, outcome, observed_at);

CREATE TABLE IF NOT EXISTS wallet_history_backfills (
    wallet_id                  uuid PRIMARY KEY REFERENCES wallets(id) ON DELETE CASCADE,
    trades_fetched             int NOT NULL DEFAULT 0,
    closed_positions_fetched   int NOT NULL DEFAULT 0,
    trades_complete            boolean NOT NULL DEFAULT false,
    closed_positions_complete  boolean NOT NULL DEFAULT false,
    last_backfilled_at         timestamptz,
    last_error                 text,
    raw_stats                  jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at                 timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_whb_complete
    ON wallet_history_backfills(closed_positions_complete, trades_complete, last_backfilled_at DESC);
