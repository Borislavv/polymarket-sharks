# Env reference

## Required (no defaults; service fails fast if missing)

| key | purpose |
|---|---|
| `DATABASE_URL` | Postgres DSN (e.g. `postgres://user:pass@host:5432/db?sslmode=disable`) |
| `TELEGRAM_BOT_TOKEN` | Bot token from `@BotFather` |
| `TELEGRAM_ADMIN_CHAT_ID` | admin channel for diagnostics and BLOCKING-risk alerts |
| `TELEGRAM_BETS_CHAT_ID` | SHARK / INSIDER alerts |
| `TELEGRAM_CLUSTERS_CHAT_ID` | CLUSTER alerts |
| `TELEGRAM_NEWS_CHAT_ID` | NEWS alerts (only when `NEXTJS_NEWS_ENABLED=true`) |

## Polymarket base URLs (have safe defaults)

| key | default |
|---|---|
| `POLYMARKET_GAMMA_BASE_URL` | `https://gamma-api.polymarket.com` |
| `POLYMARKET_DATA_API_BASE_URL` | `https://data-api.polymarket.com` |
| `POLYMARKET_CLOB_BASE_URL` | `https://clob.polymarket.com` |
| `POLYMARKET_WS_URL` | `wss://ws-subscriptions-clob.polymarket.com/ws/market` |

## Intervals (Go duration: `30s`, `2m`, `1h`)

| key | default | notes |
|---|---|---|
| `DISCOVERY_INTERVAL` | `5m` | how often Gamma is polled |
| `HOLDER_SCAN_INTERVAL` | `10m` | top-holder scan over hotset |
| `WATCHED_WALLET_POLL_INTERVAL` | `1m` | `/activity` poll cadence |
| `CLUSTER_SCAN_INTERVAL` | `90s` | cluster scan + hotset refresh |
| `NEWS_SCAN_INTERVAL` | `5m` | Next.js news polling |

## Hotset

| key | default |
|---|---|
| `HOTSET_MAX_MARKETS` | `80` |

## Shark strategy

| key | default | meaning |
|---|---|---|
| `SHARK_MIN_TRADES` | `100` | sample-size gate |
| `SHARK_MIN_CLOSED_POSITIONS` | `30` | alternative sample-size gate |
| `SHARK_MIN_SCORE` | `70` | promotion threshold |
| `SHARK_MIN_CONFIDENCE` | `0.65` | confidence floor for promotion |
| `SHARK_MAX_STALE_DAYS` | `21` | last-trade staleness cap |

## Insider strategy

| key | default |
|---|---|
| `INSIDER_MAX_LIFETIME_TRADES` | `3` |
| `INSIDER_MAX_LIFETIME_MARKETS` | `3` |
| `INSIDER_MIN_NOTIONAL_USD` | `19000` |
| `INSIDER_MIN_SCORE` | `70` |
| `INSIDER_MIN_CONFIDENCE` | `0.60` |
| `INSIDER_LOW_PROB_PRICE_THRESHOLD` | `0.20` |

## Weekly lucky-spike strategy (admin-first)

Scans all active markets and flags wallets with sustained weekly
high-frequency trading plus elevated realized profit percentage.

| key | default | notes |
|---|---|---|
| `LUCKY_SPIKE_ENABLED` | `false` | feature flag |
| `LUCKY_SPIKE_INTERVAL` | `30m` | full scan cadence |
| `LUCKY_SPIKE_MAX_MARKETS` | `0` | `0` means all active markets |
| `LUCKY_SPIKE_MARKET_TRADES_LIMIT` | `40` | legacy per-market fallback sample size |
| `LUCKY_SPIKE_MARKET_CONCURRENCY` | `8` | legacy per-market fallback concurrency |
| `LUCKY_SPIKE_CANDIDATE_TRADE_PAGE_SIZE` | `500` | global `/trades` page size for streaming candidate discovery |
| `LUCKY_SPIKE_CANDIDATE_TRADE_MAX_PAGES` | `120` | max global `/trades` pages per cycle |
| `LUCKY_SPIKE_CANDIDATE_MIN_SAMPLE_TRADES` | `6` | recent global-stream trades needed before a wallet is evaluated |
| `LUCKY_SPIKE_MAX_CANDIDATE_WALLETS` | `2000` | cap per cycle |
| `LUCKY_SPIKE_WALLET_CONCURRENCY` | `6` | concurrent wallet evaluations |
| `LUCKY_SPIKE_WALLET_TRADE_PAGE_SIZE` | `500` | per-page wallet history / positions fetch |
| `LUCKY_SPIKE_WALLET_TRADE_MAX_PAGES` | `10` | positions/P&L pagination safety cap |
| `LUCKY_SPIKE_WALLET_ACTIVITY_MAX_PAGES` | `90` | `/activity` history pages per wallet; after offset `3000`, crawler continues with `end=<oldest_ts-1>` |
| `LUCKY_SPIKE_PER_WALLET_TIMEOUT` | `120s` | timeout per wallet evaluation |
| `LUCKY_SPIKE_LOOKBACK` | `168h` | weekly window (7 days) |
| `LUCKY_SPIKE_MAX_AVG_TRADE_INTERVAL` | `2m` | frequency gate |
| `LUCKY_SPIKE_MIN_PROFIT_PCT` | `0.30` | strict gate is `profit_pct > 0.30` |
| `LUCKY_SPIKE_MIN_TRADES_PER_WEEK` | `5040` | 7d / 2m |
| `LUCKY_SPIKE_MIN_TRADES_PER_MONTH` | `21600` | 30d / 2m |
| `LUCKY_SPIKE_MIN_COVERAGE` | `144h` | activity span floor (6 days) |
| `LUCKY_SPIKE_MIN_OBSERVED_TRADES` | `1000` | cap-aware lower-bound sample size when Data API history is truncated |
| `LUCKY_SPIKE_MIN_OBSERVED_COVERAGE` | `48h` | cap-aware lower-bound observed span |
| `LUCKY_SPIKE_MIN_ENTRY_NOTIONAL` | `0` | optional realized entry-notional floor for profit gate |
| `LUCKY_SPIKE_MIN_REALIZED_PNL` | `0` | optional realized PnL floor for profit gate |
| `LUCKY_SPIKE_MIN_REALIZED_CYCLES` | `30` | min Polymarket position sample (reconstructed cycles are diagnostic fallback only) |
| `LUCKY_SPIKE_MIN_SCORE` | `75` | promotion threshold inside strategy |
| `LUCKY_SPIKE_MIN_CONFIDENCE` | `0.70` | confidence floor |

## MLB late-game match strategy (admin-first)

Scans live MLB/baseball games and flags active Polymarket match markets when
the away team is batting in top 9+ or extras while trailing by at least two
runs. This is a market-timing signal, not a trader-ranking strategy.

| key | default | notes |
|---|---|---|
| `MLB_LATE_GAME_ENABLED` | `false` | feature flag |
| `MLB_LATE_GAME_INTERVAL` | `30s` | scoreboard + market matching cadence |
| `MLB_LATE_GAME_MIN_INNING` | `9` | `9` means top 9th and top extra innings |
| `MLB_LATE_GAME_MIN_AWAY_DEFICIT` | `2` | away team must trail by at least this many runs |
| `MLB_LATE_GAME_MARKET_LIMIT` | `0` | `0` means scan all active markets in local DB |
| `MLB_STATS_API_BASE_URL` | `https://statsapi.mlb.com` | public MLB Stats API base URL |

## Retention / storage safety

Hard row-cap worker for high-volume derived/diagnostic tables. It does not
delete `wallets`, `wallet_watchlist`, `watched_bets`, lifecycle/exit rows,
alerts, Telegram deliveries, markets/events, or watched trader identity.

Hot-path closed-position scoring reads `wallet_closed_position_latest`, a
compact latest-state table. The older `wallet_closed_positions` table is
treated as legacy high-volume storage and retained independently, so startup
and scoring do not need to scan the historical snapshot table.

`wallet_closed_positions` has a special first phase: each sweep deletes only
legacy duplicates and keeps the latest row per `(wallet_id, condition_id,
outcome)`. The global row cap for that table is applied only after a cycle
where no duplicate rows were deleted, so latest-state correctness wins over
raw space pressure.

| key | default | notes |
|---|---|---|
| `RETENTION_ENABLED` | `false` | feature flag |
| `RETENTION_INTERVAL` | `1m` | sweep cadence; first sweep runs immediately |
| `RETENTION_PER_TABLE_TIMEOUT` | `45s` | timeout per prune statement |
| `RETENTION_BATCH_SIZE` | `50000` | max rows deleted per table/reason per sweep |
| `RETENTION_WALLET_CLOSED_POSITIONS_MAX_ROWS` | `2000000` | `0` disables cap; watchlist wallets are protected |
| `RETENTION_MARKET_PRICE_SAMPLES_MAX_ROWS` | `1000000` | oldest samples removed first |
| `RETENTION_HOLDER_SNAPSHOTS_MAX_ROWS` | `250000` | oldest snapshots removed first |
| `RETENTION_CANDIDATE_EVIDENCE_MAX_ROWS` | `250000` | non-watchlist evidence only |
| `RETENTION_WALLET_SCORES_MAX_ROWS` | `500000` | non-watchlist, non-promoted scores only; latest per wallet/strategy protected |

## Cluster

| key | default |
|---|---|
| `CLUSTER_WINDOW_BEFORE` | `3h` |
| `CLUSTER_WINDOW_AFTER` | `3h` |
| `CLUSTER_MIN_WALLETS` | `2` |
| `CLUSTER_MIN_TOTAL_NOTIONAL_USD` | `5000` |
| `CLUSTER_MIN_QUALITY_SCORE` | `60` |

## News (Next.js, OPTIONAL)

| key | default | notes |
|---|---|---|
| `NEXTJS_NEWS_ENABLED` | `false` | OFF by default; internal source |
| `NEXTJS_BUILD_ID_TTL` | `30m` | cache lifetime for resolved buildId |

## Rate limits (req/sec)

| key | default |
|---|---|
| `GAMMA_RPS_LIMIT` | `8` |
| `DATA_API_RPS_LIMIT` | `8` |
| `CLOB_RPS_LIMIT` | `8` |
| `TELEGRAM_RPS_LIMIT` | `1` |

## Price sampler (CLV proxy)

| key | default | required | notes |
|---|---|---|---|
| `PRICE_SAMPLER_INTERVAL` | `2m` | no | how often hotset tokens are polled for CLOB book/midpoint |
| `PRICE_SAMPLER_MAX_PER_CYCLE` | `40` | no | upper bound on tokens probed per cycle |

## Telegram retry / safety

| key | default | required | notes |
|---|---|---|---|
| `TELEGRAM_RETRY_INTERVAL` | `30s` | no | scan-cadence for failed deliveries |
| `TELEGRAM_MAX_ATTEMPTS` | `5` | no | cap on retry attempts per decision |
| `ALERTING_ENABLED` | `true` | no | master kill-switch. **Set `false` in staging** to persist decisions+deliveries (`status='skipped'`) without hitting Telegram. |

## Position lifecycle / exit alerts

Tracks watched-wallet positions from opening BUY to selling/closing SELL.

| key | default | required | notes |
|---|---|---|---|
| `LIFECYCLE_ENABLED` | `true` | no | turn off to fully bypass lifecycle tracking |
| `EXIT_ALERTS_ENABLED` | `true` | no | when true, EXIT decisions are routed to `TELEGRAM_BETS_CHAT_ID`; when false, lifecycle is still recorded silently |
| `EXIT_FULL_CLOSE_TOLERANCE` | `0.05` | no | remaining-size fraction considered "closed" (5% slack) |
| `EXIT_CLUSTER_ENABLED` | `false` | no | include exit trades in cluster scans (default: off — clusters track entries) |

## Runtime

| key | default |
|---|---|
| `HTTP_TIMEOUT` | `20s` |
| `WORKER_CONCURRENCY` | `6` |
| `LOG_LEVEL` | `info` (debug/info/warn/error) |
| `METRICS_ADDR` | `:9090` (Prometheus-ish at `/metrics`) |

## Categories / extras

| key | default |
|---|---|
| `TARGET_CATEGORIES` | `all` (`all`/`*` = no category filter; or comma-separated slugs) |
| `INTERNAL_DASHBOARD_BASE_URL` | empty; optional internal dashboard link base |
