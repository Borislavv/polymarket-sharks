-- migration 0012: wallet_closed_positions → latest-state upsert model.
--
-- Background: 0009 declared wallet_closed_positions as append-only snapshot
-- history, with observed_at participating in the unique key. The backfill
-- worker re-fetches every wallet every ~6h and inserts a fresh row per
-- (wallet, condition, outcome) on each cycle. With observed_at = now() this
-- defeats ON CONFLICT and produces ~90× row duplication; a 28k-wallet deploy
-- accumulated 50M rows / 81 GB in weeks. Scoring already reads only the
-- latest row per key (DISTINCT ON observed_at DESC), so historical multiples
-- carry no signal — they are pure storage cost.
--
-- New model: one current row per (wallet_id, condition_id, outcome). Each
-- refresh either updates the existing row in place or inserts a new one.
-- first_seen_at preserves the earliest observation; last_seen_at advances
-- every cycle the row is re-confirmed.
--
-- This file is idempotent and safe to run on a clean test DB. On the
-- production 81 GB DB the unique-index creation will fail until the
-- operator-run cleanup script (docs/cleanup-closed-positions.sql) has
-- deduplicated existing rows; the DO block below swallows that error so
-- watchtower can boot against either state. Once cleanup completes the
-- operator runs the same CREATE UNIQUE INDEX manually.

ALTER TABLE wallet_closed_positions
    ADD COLUMN IF NOT EXISTS first_seen_at timestamptz;
ALTER TABLE wallet_closed_positions
    ADD COLUMN IF NOT EXISTS last_seen_at  timestamptz;

-- Old unique key was (wallet_id, condition_id, outcome, observed_at) where
-- observed_at = now() — never collided. Drop it; the column stays for
-- compatibility (it now represents the most recent observation).
DROP INDEX IF EXISTS uq_wcp_wallet_position_observed;

-- New unique key matches the application's actual identity for a closed
-- position. Migrations are executed on every service start in this project, so
-- never attempt a heavyweight unique-index build on a large production table
-- during boot. Clean/small DBs still get the invariant immediately; large DBs
-- are deduplicated by the retention worker/operator cleanup first, then the
-- unique index can be created manually/concurrently.
DO $migration_0012$
DECLARE
    row_estimate bigint;
BEGIN
    IF to_regclass('public.uq_wcp_wallet_position') IS NOT NULL THEN
        RETURN;
    END IF;

    SELECT GREATEST(COALESCE(reltuples, 0), 0)::bigint
      INTO row_estimate
      FROM pg_class
     WHERE oid = 'wallet_closed_positions'::regclass;

    IF row_estimate <= 1000000 THEN
        BEGIN
            CREATE UNIQUE INDEX uq_wcp_wallet_position
                ON wallet_closed_positions(wallet_id, condition_id, outcome);
        EXCEPTION
            WHEN unique_violation THEN
                RAISE NOTICE 'wallet_closed_positions has duplicate keys; unique index uq_wcp_wallet_position NOT created. Run retention/operator cleanup then create the index manually.';
            WHEN duplicate_table THEN
                NULL;
        END;
    ELSE
        RAISE NOTICE 'wallet_closed_positions row estimate % is too high for startup unique-index build; retention/operator cleanup must deduplicate first.', row_estimate;
    END IF;
END;
$migration_0012$;
