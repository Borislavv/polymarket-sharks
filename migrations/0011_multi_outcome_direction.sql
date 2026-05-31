-- Migration 0011: multi-outcome direction support
-- Adds outcome, direction_outcome, side to watched_bets so categorical trades
-- (NBA teams, UP/DOWN, etc.) are stored with full directional context.
-- Also adds direction_outcome to bet_clusters for categorical cluster grouping.

ALTER TABLE watched_bets
    ADD COLUMN IF NOT EXISTS outcome           text,
    ADD COLUMN IF NOT EXISTS direction_outcome text,
    ADD COLUMN IF NOT EXISTS side              text;

-- Index for cluster detection: group by market + direction + direction_outcome.
CREATE INDEX IF NOT EXISTS idx_watched_bets_cluster
    ON watched_bets(market_id, direction, direction_outcome, detected_at DESC);

ALTER TABLE bet_clusters
    ADD COLUMN IF NOT EXISTS direction_outcome text;

-- Back-fill: for existing binary bets, extract outcome from direction field.
-- YES_BUY / YES_SELL → outcome = 'YES'; NO_BUY / NO_SELL → outcome = 'NO'.
UPDATE watched_bets
SET
    outcome = CASE
        WHEN direction IN ('YES_BUY','YES_SELL') THEN 'YES'
        WHEN direction IN ('NO_BUY','NO_SELL')   THEN 'NO'
        ELSE NULL
    END,
    side = CASE
        WHEN direction IN ('YES_BUY','NO_BUY','OUTCOME_BUY')   THEN 'BUY'
        WHEN direction IN ('YES_SELL','NO_SELL','OUTCOME_SELL') THEN 'SELL'
        ELSE NULL
    END
WHERE outcome IS NULL;
