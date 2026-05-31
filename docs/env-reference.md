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
| `TARGET_CATEGORIES` | `politics,geopolitics,war,military,elections` (comma-separated) |
| `INTERNAL_DASHBOARD_BASE_URL` | empty; optional internal dashboard link base |
