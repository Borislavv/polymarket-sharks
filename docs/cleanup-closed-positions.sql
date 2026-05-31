-- Operator-run cleanup for wallet_closed_positions.
--
-- Background: before migration 0012 the table accumulated ~90× duplicates
-- (observed_at participated in the unique key with DEFAULT now(), so every
-- backfill cycle inserted a fresh row per closed position). After 0012
-- ships, new writes are upserts; this script collapses the legacy duplicate
-- rows and enforces the new unique key.
--
-- DO NOT run without:
--   1. A current pg_dump backup of the table (Step 0).
--   2. Sufficient free disk to roughly double the table during VACUUM FULL
--      (current heap ~61 GB → reserve ~80 GB headroom).
--   3. Watchtower stopped or paused (the operation takes table locks).
--   4. Explicit operator sign-off — the user must say "execute cleanup" in
--      chat or in the change ticket. This file is documentation, not a
--      migration.
--
-- Sized for the observed state on 2026-05-30:
--   db_size       = 81 GB
--   total_rows    = 49 961 401
--   distinct keys = 552 712
--   ratio         ≈ 90.4× duplication
--   expected post-vacuum size: 1–2 GB (dropping ~80 GB).

-- =====================================================================
-- Step 0 — Backup. Run from the host, NOT inside psql.
-- =====================================================================
-- docker compose exec -T postgres pg_dump -Fc -U watchtower -d watchtower \
--     -t wallet_closed_positions \
--     > wallet_closed_positions_$(date -u +%Y%m%dT%H%M%SZ).dump
--
-- Verify the dump is non-empty:
--   ls -lh wallet_closed_positions_*.dump
--   docker compose exec -T postgres pg_restore --list \
--       /dev/stdin < wallet_closed_positions_*.dump | head

-- =====================================================================
-- Step 1 — Confirm the duplicate magnitude one more time before deleting.
-- =====================================================================
SELECT
    count(*)                                                                 AS total_rows,
    count(DISTINCT (wallet_id, condition_id, outcome))                       AS distinct_keys,
    pg_size_pretty(pg_total_relation_size('wallet_closed_positions'))        AS size_before;

-- =====================================================================
-- Step 2 — Delete duplicates keeping the latest row per key.
--
-- "Latest" = observed_at DESC, then id DESC as a stable tie-breaker. The
-- window function runs in one pass; the DELETE uses the id PK so no extra
-- sort is required. Expected duration: 30–90 minutes on a 50M-row table
-- with the existing pkey + idx_wcp_wallet_market available.
--
-- Run inside an explicit transaction so the operator can ROLLBACK if the
-- pre-commit row counts look wrong.
-- =====================================================================
BEGIN;

WITH ranked AS (
    SELECT
        id,
        ROW_NUMBER() OVER (
            PARTITION BY wallet_id, condition_id, outcome
            ORDER BY observed_at DESC, id DESC
        ) AS rn
    FROM wallet_closed_positions
)
DELETE FROM wallet_closed_positions w
USING ranked r
WHERE w.id = r.id
  AND r.rn > 1;

-- Sanity check: rows remaining should equal the distinct_keys count from Step 1.
SELECT count(*) AS rows_after_dedup FROM wallet_closed_positions;

-- Backfill the timestamp columns added in migration 0012 for the survivors.
UPDATE wallet_closed_positions
SET first_seen_at = observed_at,
    last_seen_at  = observed_at
WHERE first_seen_at IS NULL OR last_seen_at IS NULL;

COMMIT;

-- =====================================================================
-- Step 3 — Enforce the latest-state invariant going forward.
-- =====================================================================
CREATE UNIQUE INDEX IF NOT EXISTS uq_wcp_wallet_position
    ON wallet_closed_positions(wallet_id, condition_id, outcome);

-- =====================================================================
-- Step 4 — Reclaim heap + index disk back to the OS.
--
-- VACUUM FULL takes an ACCESS EXCLUSIVE lock and rewrites the table; it
-- requires free disk roughly equal to the surviving table size, which is
-- now small (~tens of MB). REINDEX is included for the same reason: after
-- 90× duplication the btree indexes are bloated.
--
-- This step is the one that actually returns the 80 GB to the Docker
-- volume. A plain VACUUM does NOT shrink on-disk files.
-- =====================================================================
VACUUM (FULL, ANALYZE) wallet_closed_positions;
REINDEX TABLE wallet_closed_positions;

-- =====================================================================
-- Step 5 — Verify.
-- =====================================================================
SELECT
    count(*)                                                          AS rows_after,
    pg_size_pretty(pg_total_relation_size('wallet_closed_positions')) AS size_after,
    pg_size_pretty(pg_database_size('watchtower'))                    AS db_size_after;

-- =====================================================================
-- Optional Step 6 — Drop the leftover idx_wcp_wallet_market index.
--
-- It is redundant with the new uq_wcp_wallet_position (same column order,
-- same leading columns). Dropping it saves another ~10 GB of index space
-- on the pre-dedup heap; post-dedup it's small but still redundant. Leave
-- in place if you prefer to keep the non-unique covering index for any
-- planner reason.
-- =====================================================================
-- DROP INDEX IF EXISTS idx_wcp_wallet_market;
