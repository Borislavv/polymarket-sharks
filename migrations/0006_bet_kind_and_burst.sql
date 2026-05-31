-- migration 0006:
--   1. mark watched_bets as entry vs exit so cluster source can filter
--      `bet_kind='entry'` and EXIT_CLUSTER_ENABLED=false actually means
--      something.
--   2. add wallet/time index for same-wallet burst scanning.
--   3. add bet_clusters.cluster_kind ('multi_wallet' | 'single_trader')
--      so the existing dedup_key surface stays disjoint between modes.

ALTER TABLE watched_bets
    ADD COLUMN IF NOT EXISTS bet_kind text NOT NULL DEFAULT 'entry';

-- backfill: SELL directions are exits, BUY are entries.
UPDATE watched_bets
SET bet_kind = 'exit'
WHERE bet_kind = 'entry'
  AND direction IN ('YES_SELL', 'NO_SELL');

CREATE INDEX IF NOT EXISTS idx_watched_bets_wallet_time
    ON watched_bets(wallet_id, detected_at DESC);
CREATE INDEX IF NOT EXISTS idx_watched_bets_kind_time
    ON watched_bets(bet_kind, detected_at DESC);

ALTER TABLE bet_clusters
    ADD COLUMN IF NOT EXISTS cluster_kind text NOT NULL DEFAULT 'multi_wallet';
