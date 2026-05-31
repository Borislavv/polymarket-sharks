# internal/marketscan

## What this module does

- `DiscoveryWorker` — pulls Gamma `/events` for each configured tag, upserts events/markets/market_tokens.
- `HotsetWorker` — maintains a bounded set of "hot" markets; syncs CLOB WS subscriptions to match.
- `HolderScanWorker` — pulls `/holders` per hotset market, ranks, persists `holder_snapshots`; deep-scans top-N markets via `/v1/market-positions`. Calls back into scoring for new wallets.
- `NewsWorker` — OPTIONAL, feature-flagged. Reads Next.js event JSON.

## Reads from

- Polymarket Gamma `/events?tag_slug=...`
- Polymarket Data API `/holders`, `/v1/market-positions`
- Polymarket Next.js (event HTML for buildId; event JSON for news)
- Postgres `markets` / `events` / `market_tokens`

## Writes to

- `events`, `markets`, `market_tokens`, `market_state`
- `wallets` (upsert on encounter)
- `holder_snapshots`
- `news_items` (via NewsWorker)
- `alert_decisions` + `telegram_deliveries` (via `alerts.Router`, NEWS channel)

## Invariants

- Discovery only fetches active/non-closed events for configured tags.
- Hotset is bounded by `HOTSET_MAX_MARKETS`; never unbounded scans.
- `holder_snapshots.pct_outcome_snapshot` is `null` if denominator unknown (POC: always null from `/holders` alone; deep scan via positions may add it later).
- NewsWorker is feature-flagged. Failures do not crash core surveillance.

## Failure modes

- Gamma 429/5xx → bounded retries via shared HTTP client; failures are logged + counter `discovery_errors_total`.
- WS reconnect with backoff up to 30s; subscription state is replayed on reconnect.
- NextJS 404 → buildId cache invalidated, retried once.

## Metrics

`wt_discovery_events_upserted_total`, `wt_discovery_markets_upserted_total`, `wt_hotset_size`, `wt_hotset_subscribes_total`, `wt_hotset_unsubscribes_total`, `wt_holders_snapshots_total`, `wt_positions_snapshots_total`, `wt_news_fetch_errors_total`, `wt_news_items_new_total`.

## How to test

Unit tests for the Next.js buildId resolver and HTTP retry behaviour live in `polymarket/dataapi` and `polymarket/nextjs`. Workers themselves are integration-tested live (DB + real Polymarket endpoints) — POC keeps unit-level coverage on pure scoring/parsing logic.
