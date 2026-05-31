-- watchtower schema v1
-- Run inside a single migration via `psql -f`. Idempotent via IF NOT EXISTS.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS events (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    polymarket_event_id  text,
    slug                 text UNIQUE NOT NULL,
    title                text NOT NULL,
    category             text,
    tags                 jsonb DEFAULT '[]'::jsonb,
    raw                  jsonb,
    active               boolean NOT NULL DEFAULT true,
    closed               boolean NOT NULL DEFAULT false,
    discovered_at        timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_events_slug ON events(slug);
CREATE INDEX IF NOT EXISTS idx_events_active ON events(active, closed);

CREATE TABLE IF NOT EXISTS markets (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    condition_id         text UNIQUE NOT NULL,
    event_id             uuid REFERENCES events(id) ON DELETE SET NULL,
    slug                 text,
    question             text,
    description          text,
    resolution_source    text,
    rules_text           text,
    active               boolean NOT NULL DEFAULT true,
    closed               boolean NOT NULL DEFAULT false,
    volume               numeric,
    liquidity            numeric,
    raw                  jsonb,
    discovered_at        timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_markets_condition_id ON markets(condition_id);
CREATE INDEX IF NOT EXISTS idx_markets_event_id ON markets(event_id);
CREATE INDEX IF NOT EXISTS idx_markets_active ON markets(active, closed);

CREATE TABLE IF NOT EXISTS market_tokens (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    market_id       uuid NOT NULL REFERENCES markets(id) ON DELETE CASCADE,
    outcome_index   int NOT NULL,
    outcome_name    text,
    clob_token_id   text UNIQUE NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_market_tokens_clob ON market_tokens(clob_token_id);
CREATE INDEX IF NOT EXISTS idx_market_tokens_market ON market_tokens(market_id);

CREATE TABLE IF NOT EXISTS market_tag_links (
    market_id   uuid NOT NULL REFERENCES markets(id) ON DELETE CASCADE,
    tag_slug    text NOT NULL,
    tag_id      text,
    source      text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (market_id, tag_slug)
);
CREATE INDEX IF NOT EXISTS idx_market_tag_links_slug ON market_tag_links(tag_slug);

CREATE TABLE IF NOT EXISTS market_state (
    market_id      uuid PRIMARY KEY REFERENCES markets(id) ON DELETE CASCADE,
    last_price     numeric,
    best_bid       numeric,
    best_ask       numeric,
    spread         numeric,
    last_trade_at  timestamptz,
    ws_seen_at     timestamptz,
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS wallets (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    proxy_wallet    text UNIQUE NOT NULL,
    pseudonym       text,
    profile_slug    text,
    first_seen_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    raw             jsonb
);
CREATE INDEX IF NOT EXISTS idx_wallets_proxy ON wallets(proxy_wallet);

CREATE TABLE IF NOT EXISTS wallet_scores (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id         uuid NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    strategy          text NOT NULL,
    class             text,
    score             int NOT NULL,
    confidence        numeric NOT NULL,
    promote           boolean NOT NULL DEFAULT false,
    score_version     text NOT NULL,
    feature_snapshot  jsonb NOT NULL,
    reason_codes      text[] NOT NULL DEFAULT '{}',
    missing_data      text[] NOT NULL DEFAULT '{}',
    calculated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_wallet_scores_lookup
    ON wallet_scores(wallet_id, strategy, calculated_at DESC);

CREATE TABLE IF NOT EXISTS wallet_watchlist (
    wallet_id         uuid PRIMARY KEY REFERENCES wallets(id) ON DELETE CASCADE,
    class             text NOT NULL,
    status            text NOT NULL,
    score             int NOT NULL,
    confidence        numeric NOT NULL,
    reason_codes      text[] NOT NULL DEFAULT '{}',
    feature_snapshot  jsonb NOT NULL,
    score_version     text NOT NULL,
    promoted_at       timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_wallet_watchlist_status_class
    ON wallet_watchlist(status, class);

CREATE TABLE IF NOT EXISTS holder_snapshots (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    market_id             uuid NOT NULL REFERENCES markets(id) ON DELETE CASCADE,
    token_id              uuid REFERENCES market_tokens(id) ON DELETE SET NULL,
    wallet_id             uuid NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    outcome_index         int,
    amount                numeric,
    rank                  int,
    pct_outcome_snapshot  numeric,
    snapshot_at           timestamptz NOT NULL DEFAULT now(),
    source                text,
    raw                   jsonb
);
CREATE INDEX IF NOT EXISTS idx_holder_snapshots_market
    ON holder_snapshots(market_id, snapshot_at DESC);

CREATE TABLE IF NOT EXISTS wallet_trades (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_hash   text NOT NULL,
    wallet_id          uuid NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    market_id          uuid REFERENCES markets(id) ON DELETE SET NULL,
    event_slug         text,
    condition_id       text,
    outcome            text,
    side               text,
    direction          text,
    price              numeric,
    size               numeric,
    usdc_size          numeric,
    timestamp          timestamptz NOT NULL,
    source             text,
    raw                jsonb,
    CONSTRAINT u_trade UNIQUE (transaction_hash, wallet_id, condition_id, outcome, side)
);
CREATE INDEX IF NOT EXISTS idx_wallet_trades_tx ON wallet_trades(transaction_hash);
CREATE INDEX IF NOT EXISTS idx_wallet_trades_wallet ON wallet_trades(wallet_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_wallet_trades_market ON wallet_trades(market_id, timestamp DESC);

CREATE TABLE IF NOT EXISTS watched_bets (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_trade_id   uuid NOT NULL REFERENCES wallet_trades(id) ON DELETE CASCADE,
    wallet_id         uuid NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    market_id         uuid REFERENCES markets(id) ON DELETE SET NULL,
    direction         text NOT NULL,
    notional          numeric,
    price             numeric,
    odds              numeric,
    payoff_if_win     numeric,
    wallet_class      text,
    wallet_score      int,
    reason_codes      text[] NOT NULL DEFAULT '{}',
    feature_snapshot  jsonb NOT NULL,
    detected_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_watched_bets_market ON watched_bets(market_id, direction, detected_at DESC);
CREATE INDEX IF NOT EXISTS idx_watched_bets_wallet ON watched_bets(wallet_id, detected_at DESC);

CREATE TABLE IF NOT EXISTS bet_clusters (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    market_id              uuid REFERENCES markets(id) ON DELETE SET NULL,
    event_id               uuid REFERENCES events(id) ON DELETE SET NULL,
    direction              text NOT NULL,
    window_start           timestamptz NOT NULL,
    window_end             timestamptz NOT NULL,
    wallet_count           int NOT NULL,
    total_notional         numeric NOT NULL,
    weighted_price         numeric,
    average_odds           numeric,
    payoff_if_win_total    numeric,
    cluster_score          int NOT NULL,
    watched_bet_ids        jsonb NOT NULL DEFAULT '[]'::jsonb,
    wallet_ids             jsonb NOT NULL DEFAULT '[]'::jsonb,
    reason_codes           text[] NOT NULL DEFAULT '{}',
    feature_snapshot       jsonb NOT NULL,
    dedup_key              text UNIQUE NOT NULL,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_bet_clusters_dedup ON bet_clusters(dedup_key);

CREATE TABLE IF NOT EXISTS news_items (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id        uuid REFERENCES events(id) ON DELETE SET NULL,
    event_slug      text,
    title           text NOT NULL,
    summary         text,
    source_url      text,
    news_timestamp  timestamptz,
    raw             jsonb,
    fingerprint     text UNIQUE NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_news_items_fp ON news_items(fingerprint);

CREATE TABLE IF NOT EXISTS alert_decisions (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    alert_type            text NOT NULL,
    entity_type           text NOT NULL,
    entity_id             text NOT NULL,
    severity              text NOT NULL,
    should_send           boolean NOT NULL,
    user_alert_allowed    boolean NOT NULL,
    admin_alert_allowed   boolean NOT NULL,
    reason_codes          text[] NOT NULL DEFAULT '{}',
    missing_data          text[] NOT NULL DEFAULT '{}',
    feature_snapshot      jsonb NOT NULL,
    dedup_key             text UNIQUE NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_alert_decisions_dedup ON alert_decisions(dedup_key);

CREATE TABLE IF NOT EXISTS telegram_deliveries (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    alert_decision_id    uuid NOT NULL REFERENCES alert_decisions(id) ON DELETE CASCADE,
    chat_id              text NOT NULL,
    status               text NOT NULL,
    telegram_message_id  text,
    error                text,
    sent_at              timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_telegram_deliveries_decision
    ON telegram_deliveries(alert_decision_id);
