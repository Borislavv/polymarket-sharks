# Architecture

## One binary, five surfaces

```
                 +-----------------+        +-------------+
   Polymarket ---|  HTTP clients   |        | Telegram    |
   (Gamma /      |  + rate limits  |        | Bot API     |
    Data API /   |  + retries      |        +------+------+
    CLOB /       +--------+--------+               ^
    Next.js)             |                         |
                          v                        |
   +------------------------------------------------------+
   |                       app.Run                        |
   |                                                      |
   |  +-----------+   +----------+   +-----------------+ |
   |  | discovery |   | hotset   |   | holder scan     | |
   |  +-----+-----+   +----+-----+   +-------+---------+ |
   |        |              |                 |           |
   |        v              v                 v           |
   |   +--------------- postgres (pgxpool) ----------+   |
   |   | events / markets / market_tokens / state    |   |
   |   | wallets / wallet_scores / wallet_watchlist  |   |
   |   | holder_snapshots / wallet_trades            |   |
   |   | watched_bets / bet_clusters / news_items    |   |
   |   | alert_decisions / telegram_deliveries       |   |
   |   +---+----------------------------------------+    |
   |       ^                                             |
   |       |       +--------------+                      |
   |       +-------| walletintel  |  scoring runner      |
   |               | shark/insider|                      |
   |               | rules/arbitr |                      |
   |               +------+-------+                      |
   |                      |                              |
   |                      v                              |
   |               +-------------+                       |
   |               | AlertRouter |---> Telegram (only)   |
   |               +-------------+                       |
   +------------------------------------------------------+
```

## Process boundaries

- All workers receive `context.Context` and stop on cancel.
- All HTTP requests have per-surface rate limiters and bounded retries (429/5xx only).
- The single Telegram client lives inside `AlertRouter` — no worker may call it directly.
- DB writes are idempotent: trades unique on `(tx_hash, wallet, condition, outcome, side)`, alerts unique on `dedup_key`, news on `fingerprint`, clusters on `dedup_key`.

## Why this shape

- One binary keeps deployment, observability, and ops trivial.
- No broker (Redis/Kafka) — Postgres + bounded goroutines is sufficient for the throughput a POC needs.
- No ORM — pgx with hand-written upserts keeps semantics auditable. Migrations are `IF NOT EXISTS` additive SQL.
- No generic strategy framework — strategies are explicit Go functions in `walletintel`.
