-- migration 0002: persist Gamma rules-risk fields on markets/events for use
-- by walletintel.rules_risk_modifier. Additive, idempotent.

ALTER TABLE markets ADD COLUMN IF NOT EXISTS neg_risk              boolean;
ALTER TABLE markets ADD COLUMN IF NOT EXISTS uma_resolution_status text;
ALTER TABLE markets ADD COLUMN IF NOT EXISTS uma_resolved          boolean;
ALTER TABLE markets ADD COLUMN IF NOT EXISTS uma_bond              numeric;
ALTER TABLE markets ADD COLUMN IF NOT EXISTS end_date              timestamptz;
ALTER TABLE markets ADD COLUMN IF NOT EXISTS start_date            timestamptz;

ALTER TABLE events  ADD COLUMN IF NOT EXISTS neg_risk              boolean;
ALTER TABLE events  ADD COLUMN IF NOT EXISTS uma_uncertainty       boolean;
ALTER TABLE events  ADD COLUMN IF NOT EXISTS end_date              timestamptz;

CREATE INDEX IF NOT EXISTS idx_markets_end_date ON markets(end_date);
