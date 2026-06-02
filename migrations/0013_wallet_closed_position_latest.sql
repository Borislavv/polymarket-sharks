-- migration 0013: narrow latest-state table for closed-position scoring.
--
-- wallet_closed_positions is now a legacy high-volume snapshot table. It can
-- be retained aggressively, while hot-path scoring/backfill reads and writes
-- this compact latest-state table.

CREATE TABLE IF NOT EXISTS wallet_closed_position_latest (
    id                   bigserial PRIMARY KEY,
    wallet_id            uuid NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    condition_id         text NOT NULL,
    market_id            uuid REFERENCES markets(id) ON DELETE SET NULL,
    event_slug           text,
    outcome              text NOT NULL,
    outcome_index        int,
    total_bought         numeric,
    realized_pnl         numeric,
    avg_price            numeric,
    current_value        numeric,
    percent_pnl          numeric,
    percent_realized_pnl numeric,
    size_at_observation  numeric,
    is_closed            boolean NOT NULL,
    closed_at            timestamptz,
    observed_at          timestamptz NOT NULL DEFAULT now(),
    first_seen_at        timestamptz NOT NULL DEFAULT now(),
    last_seen_at         timestamptz NOT NULL DEFAULT now(),
    raw                  jsonb NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT uq_wcpl_wallet_position UNIQUE (wallet_id, condition_id, outcome)
);

CREATE INDEX IF NOT EXISTS idx_wcpl_wallet_time
    ON wallet_closed_position_latest(wallet_id, last_seen_at DESC);

CREATE INDEX IF NOT EXISTS idx_wcpl_wallet_closed
    ON wallet_closed_position_latest(wallet_id, is_closed, closed_at DESC);
