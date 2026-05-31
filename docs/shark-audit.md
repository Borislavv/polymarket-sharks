# Shark audit (v3.0.0 whale-only)

This file documents the exact SQL queries used to verify the whale-only
shark strategy in production. All queries are read-only; run them via
`docker compose exec postgres psql -U watchtower -d watchtower -f ...` or
paste line-by-line.

## A. Watchlist health

```sql
-- 1. Counts by class/status/score_version. Active sharks should all be
--    on the current score_version.
SELECT class, status, score_version, count(*)
FROM wallet_watchlist
GROUP BY class, status, score_version
ORDER BY count(*) DESC;
```

```sql
-- 2. Active sharks listed in detail; verify q_count >= 100, q_avg >= 20k
--    OR elite_win_rate >= 0.80 AND elite_avg_entry_notional >= 100k AND
--    elite_roi >= 0.3333. promotion_path tells you which route.
SELECT
    w.proxy_wallet,
    ww.score,
    ww.confidence,
    ww.score_version,
    (ww.feature_snapshot->>'promotion_path')              AS path,
    (ww.feature_snapshot->>'qualifying_trade_count')      AS q_count,
    (ww.feature_snapshot->>'qualifying_avg_notional')     AS q_avg,
    (ww.feature_snapshot->>'realized_pnl')                AS realized_pnl,
    (ww.feature_snapshot->>'elite_win_rate')              AS elite_win_rate,
    (ww.feature_snapshot->>'elite_avg_entry_notional')    AS elite_avg_stake,
    (ww.feature_snapshot->>'elite_total_entry_notional')  AS elite_total_notional,
    (ww.feature_snapshot->>'elite_roi')                   AS elite_roi,
    (ww.feature_snapshot->>'whale_priority_score')        AS whale_priority
FROM wallet_watchlist ww
JOIN wallets w ON w.id = ww.wallet_id
WHERE ww.class = 'shark' AND ww.status = 'active'
ORDER BY (ww.feature_snapshot->>'whale_priority_score')::numeric DESC NULLS LAST;
```

```sql
-- 3. CRITICAL: active sharks failing both normal & elite gates. Must be 0.
SELECT count(*)
FROM wallet_watchlist ww
WHERE ww.class = 'shark' AND ww.status = 'active'
  AND NOT (
    -- normal whale path
    ((ww.feature_snapshot->>'qualifying_trade_count')::numeric >= 100
     AND (ww.feature_snapshot->>'qualifying_avg_notional')::numeric >= 20000
     AND COALESCE((ww.feature_snapshot->>'partial_trade_history')::boolean, false) = false)
    OR
    -- elite high-win path
    ((ww.feature_snapshot->>'elite_win_rate')::numeric >= 0.80
     AND (ww.feature_snapshot->>'elite_avg_entry_notional')::numeric >= 100000
     AND (ww.feature_snapshot->>'elite_total_entry_notional')::numeric >= 2500000
     AND (ww.feature_snapshot->>'elite_roi')::numeric >= 0.3333)
  );
```

## B. Alert hygiene

```sql
-- 4. Dust watched_bets stored (audit only, does not gate alerts).
SELECT count(*) FROM watched_bets WHERE COALESCE(notional,0) < 20000;
```

```sql
-- 5. CRITICAL: SHARK_BET / SHARK_BURST decisions below $20k after deploy.
--    Should be 0 going forward.
SELECT count(*) FROM alert_decisions
WHERE alert_type IN ('SHARK_BET','SHARK_BURST')
  AND COALESCE((feature_snapshot->>'notional')::numeric,0) < 20000
  AND created_at >= '<deploy timestamp>';
```

## C. Candidate evidence sources

```sql
-- 6. Counts by source (holders / positions_by_volume / positions_by_pnl).
SELECT source, count(*)
FROM wallet_candidate_evidence
GROUP BY source ORDER BY count(*) DESC;
```

```sql
-- 7. Wallets that surfaced via positions_by_pnl with cash_pnl > 0 (genuine
--    profitable holders found by the new top-by-profit axis).
SELECT w.proxy_wallet, w.pseudonym, e.cash_pnl, e.realized_pnl, e.percent_pnl,
       e.current_value, e.source_rank, e.observed_at
FROM wallet_candidate_evidence e
JOIN wallets w ON w.id = e.wallet_id
WHERE e.source = 'positions_by_pnl' AND COALESCE(e.cash_pnl,0) > 0
ORDER BY e.cash_pnl DESC LIMIT 50;
```

```sql
-- 8. Top-holders without any profit evidence: they MUST NOT auto-promote.
SELECT w.proxy_wallet, count(*) AS holder_observations
FROM wallet_candidate_evidence e
JOIN wallets w ON w.id = e.wallet_id
WHERE e.source = 'holders'
  AND NOT EXISTS (
    SELECT 1 FROM wallet_candidate_evidence e2
    WHERE e2.wallet_id = e.wallet_id
      AND (COALESCE(e2.cash_pnl,0) > 0 OR COALESCE(e2.realized_pnl,0) > 0))
GROUP BY w.proxy_wallet
ORDER BY holder_observations DESC LIMIT 50;
```

## D. Sanity from raw trade history

```sql
-- 9. Wallets that look like real whales by stored trades: total notional,
--    qualifying trade count (notional >= 20k AND price <= 0.5).
SELECT
    w.proxy_wallet,
    count(*) AS trades,
    avg(COALESCE(wt.usdc_size, wt.price * wt.size)) AS avg_notional,
    sum(COALESCE(wt.usdc_size, wt.price * wt.size)) AS total_notional,
    sum(CASE WHEN wt.price <= 0.5
              AND COALESCE(wt.usdc_size, wt.price * wt.size) >= 20000
             THEN 1 ELSE 0 END) AS qualifying_20k_odds2
FROM wallet_trades wt
JOIN wallets w ON w.id = wt.wallet_id
GROUP BY w.proxy_wallet
ORDER BY total_notional DESC LIMIT 50;
```

## E. Elite path verification

```sql
-- 10. Elite-promoted sharks: must satisfy all elite gates.
SELECT w.proxy_wallet,
       (ww.feature_snapshot->>'elite_win_rate')::numeric           AS win_rate,
       (ww.feature_snapshot->>'elite_avg_entry_notional')::numeric AS avg_stake,
       (ww.feature_snapshot->>'elite_total_entry_notional')::numeric AS total_stake,
       (ww.feature_snapshot->>'elite_roi')::numeric                AS roi,
       (ww.feature_snapshot->>'elite_avg_odds')::numeric           AS avg_odds,
       (ww.feature_snapshot->>'elite_payoff_ratio')::numeric       AS payoff_ratio,
       ww.score_version
FROM wallet_watchlist ww
JOIN wallets w ON w.id = ww.wallet_id
WHERE ww.class = 'shark' AND ww.status = 'active'
  AND ww.feature_snapshot->>'promotion_path' = 'elite_high_win_whale';
```

Expected: every row has `win_rate >= 0.80`, `avg_stake >= 100000`,
`total_stake >= 2500000`, `roi >= 0.3333`, `(avg_odds >= 2 OR payoff_ratio >= 1.3333)`.

## F. Maintenance / cleanup

```sql
-- 11. Recently demoted sharks (unactual).
SELECT w.proxy_wallet, ww.reason_codes, ww.updated_at
FROM wallet_watchlist ww
JOIN wallets w ON w.id = ww.wallet_id
WHERE ww.status = 'unactual'
ORDER BY ww.updated_at DESC LIMIT 20;
```

```sql
-- 12. Old candidate evidence retention (delete >= 30 days as needed).
DELETE FROM wallet_candidate_evidence
WHERE observed_at < now() - interval '30 days';
```
