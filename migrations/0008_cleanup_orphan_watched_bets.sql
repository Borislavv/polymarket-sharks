-- migration 0008: idempotent cleanup of orphan watched_bets from the
-- pre-v3 incident. Safe because:
--   * matched rows have notional < 20_000 (whale threshold) AND
--   * matched rows belong to wallets that are currently rejected/unactual.
-- Production rows (active sharks, notional >= 20k) are untouched.
-- Alert decisions and telegram_deliveries are left in place for audit.

DELETE FROM watched_bets wb
USING wallet_watchlist ww
WHERE wb.wallet_id = ww.wallet_id
  AND ww.status NOT IN ('active', 'watch_only')
  AND COALESCE(wb.notional, 0) < 20000;

-- Also delete legacy watched_bets that have no matching wallet_trade row
-- (broken parent reference). Same dust safety: only sub-threshold rows.
DELETE FROM watched_bets wb
WHERE wb.notional < 20000
  AND NOT EXISTS (
    SELECT 1 FROM wallet_trades wt WHERE wt.id = wb.wallet_trade_id
  );
