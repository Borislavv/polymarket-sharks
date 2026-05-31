-- migration 0007: candidate evidence + 'unactual' watchlist status.
--   * wallet_candidate_evidence: every wallet that surfaced via /holders or
--     /v1/market-positions (volume / pnl sorted) is recorded here with the
--     exact numbers used by scoring. Scoring may only promote wallets with
--     real evidence (no top-holder-alone shortcuts).
--   * 'unactual' is a new wallet_watchlist.status meaning "previously
--     active shark, now fails current gates". Used by ReconcileFailedSharks.

CREATE TABLE IF NOT EXISTS wallet_candidate_evidence (
    id                 bigserial PRIMARY KEY,
    wallet_id          uuid NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    market_id          uuid REFERENCES markets(id) ON DELETE SET NULL,
    source             text NOT NULL,         -- holders|positions_by_volume|positions_by_pnl|leaderboard
    source_rank        int,
    current_value      numeric,
    total_bought       numeric,
    cash_pnl           numeric,
    realized_pnl       numeric,
    percent_pnl        numeric,
    holder_amount      numeric,
    evidence_snapshot  jsonb NOT NULL DEFAULT '{}'::jsonb,
    observed_at        timestamptz NOT NULL DEFAULT now(),
    raw                jsonb
);
CREATE INDEX IF NOT EXISTS idx_wce_wallet_time  ON wallet_candidate_evidence(wallet_id, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_wce_market_src   ON wallet_candidate_evidence(market_id, source, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_wce_source_rank  ON wallet_candidate_evidence(source, source_rank);
