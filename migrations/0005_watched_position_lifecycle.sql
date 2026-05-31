-- migration 0005: open → exit lifecycle for watched bets.
-- A lifecycle row tracks one watched-wallet position over time. New bets
-- open a row; subsequent opposite-side trades reduce or close it and emit
-- POSITION_EXIT alerts.

CREATE TABLE IF NOT EXISTS watched_position_lifecycle (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id           uuid NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    market_id           uuid REFERENCES markets(id) ON DELETE SET NULL,
    condition_id        text NOT NULL,
    outcome             text NOT NULL,
    opened_direction    text NOT NULL,
    opened_side         text NOT NULL,
    open_trade_id       uuid REFERENCES wallet_trades(id) ON DELETE SET NULL,
    open_transaction_hash text NOT NULL,
    open_notional       numeric NOT NULL,
    open_price          numeric NOT NULL,
    open_size           numeric,
    status              text NOT NULL DEFAULT 'open', -- open | partially_exited | closed
    exited_size         numeric NOT NULL DEFAULT 0,
    exit_notional       numeric NOT NULL DEFAULT 0,
    avg_exit_price      numeric,
    realized_pnl        numeric,
    opened_at           timestamptz NOT NULL,
    last_exit_at        timestamptz,
    closed_at           timestamptz,
    feature_snapshot    jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT u_open_lifecycle UNIQUE (wallet_id, condition_id, outcome, open_transaction_hash)
);
CREATE INDEX IF NOT EXISTS idx_lifecycle_active
    ON watched_position_lifecycle(wallet_id, condition_id, outcome)
    WHERE status IN ('open','partially_exited');
CREATE INDEX IF NOT EXISTS idx_lifecycle_market
    ON watched_position_lifecycle(market_id, status);

CREATE TABLE IF NOT EXISTS watched_position_exits (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    lifecycle_id        uuid NOT NULL REFERENCES watched_position_lifecycle(id) ON DELETE CASCADE,
    wallet_trade_id     uuid REFERENCES wallet_trades(id) ON DELETE SET NULL,
    transaction_hash    text NOT NULL,
    side                text NOT NULL,
    price               numeric,
    size                numeric,
    notional            numeric,
    pnl_estimate        numeric,
    detected_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT u_exit_per_tx UNIQUE (lifecycle_id, transaction_hash)
);
CREATE INDEX IF NOT EXISTS idx_exits_lifecycle ON watched_position_exits(lifecycle_id);
