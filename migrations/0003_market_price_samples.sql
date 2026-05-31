-- migration 0003: market_price_samples used by CLV proxy and trade-drift
-- diagnostics. Bounded retention is the caller's responsibility.

CREATE TABLE IF NOT EXISTS market_price_samples (
    id           bigserial PRIMARY KEY,
    market_id    uuid NOT NULL REFERENCES markets(id) ON DELETE CASCADE,
    token_id     uuid REFERENCES market_tokens(id) ON DELETE SET NULL,
    outcome      text,
    price        numeric,
    midpoint     numeric,
    best_bid     numeric,
    best_ask     numeric,
    sampled_at   timestamptz NOT NULL DEFAULT now(),
    source       text NOT NULL,
    raw          jsonb
);
CREATE INDEX IF NOT EXISTS idx_mps_market_time
    ON market_price_samples(market_id, sampled_at DESC);
CREATE INDEX IF NOT EXISTS idx_mps_token_time
    ON market_price_samples(token_id, sampled_at DESC);

-- telegram_deliveries: add attempt counter for retry worker
ALTER TABLE telegram_deliveries ADD COLUMN IF NOT EXISTS attempt int NOT NULL DEFAULT 1;
ALTER TABLE telegram_deliveries ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz;
CREATE INDEX IF NOT EXISTS idx_tg_deliveries_retry
    ON telegram_deliveries(status, next_attempt_at)
    WHERE status = 'failed';
