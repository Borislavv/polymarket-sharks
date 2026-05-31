# watchtower

Single-binary Go service that monitors Polymarket events/markets, scores wallets, detects clusters of aligned bets among watched wallets, and alerts to Telegram. Read-only surveillance — no trading, no private keys.

## What it does

1. **Discovery**: pulls events/markets from Gamma for configured categories (politics, geopolitics, war, military, elections).
2. **Hotset**: keeps a bounded list of high-attention markets and subscribes to their CLOB WS for freshness/book signals.
3. **Holder scan**: pulls top holders per hotset market; deep-scans top-N markets via `/v1/market-positions`.
4. **Scoring**: assembles wallet facts and runs four deterministic strategies:
   - `shark_score` — mature, follow-worthy traders
   - `insider_like_score` — suspicious informed-flow candidates
   - `rules_risk_modifier` — discounts ambiguous markets
   - `score_arbitration` — chooses class & severity, blocks user alerts under blocking rules risk
5. **Watchlist promotion**: persists shark/insider classifications with full feature snapshot.
6. **Watched bets**: polls `/activity` for watched wallets, enriches via `/trades` for direction.
7. **Cluster detection**: scans recent watched bets in a configurable +/-3h window, only over watchlisted wallets.
8. **Alerts**: centralized `AlertRouter` is the *only* component that sends Telegram messages.

## Quick start (Docker Compose)

```bash
cp .env.example .env
# fill in TELEGRAM_BOT_TOKEN and the four chat IDs
docker compose up -d --build
docker compose logs -f watchtower
```

Metrics at `http://localhost:9090/metrics`; health at `/healthz`.

## Local dev

```bash
go test ./...
go vet ./...
go build ./...
DATABASE_URL=postgres://... TELEGRAM_BOT_TOKEN=... \
TELEGRAM_ADMIN_CHAT_ID=... TELEGRAM_BETS_CHAT_ID=... \
TELEGRAM_CLUSTERS_CHAT_ID=... TELEGRAM_NEWS_CHAT_ID=... \
go run ./cmd/watchtower
```

## Layout

| dir | role |
|---|---|
| `cmd/watchtower` | entrypoint, signal handling |
| `internal/app` | wiring: Store, Polymarket clients, workers, router |
| `internal/config` | env loader + strict validation |
| `internal/polymarket/{gamma,dataapi,clob,nextjs}` | per-surface clients with rate limits and retries |
| `internal/storage/postgres` | pgxpool repositories (no ORM) |
| `internal/walletintel` | scoring strategies, watchlist promotion, watched/cluster workers |
| `internal/marketscan` | discovery, hotset, holder scan, news worker |
| `internal/alerts` | AlertRouter, decision/dedup, message formatters |
| `internal/telegram` | Bot-API client + MarkdownV2 escape |
| `internal/metrics` | tiny Prometheus-ish counters |
| `migrations` | SQL (idempotent) |
| `docs/` | architecture, scoring, alert UX, data flow, env reference |

## Key invariants (REQUIRED reads)

- **Direction truth source = Data API trades.** CLOB WS is *never* used to label wallet side/direction.
- **`pct_outcome_snapshot` is `null`** if denominator is unknown; never invented.
- **`alert_decisions` is persisted before Telegram send.** Send failures are recorded in `telegram_deliveries`; the decision survives for retry/audit.
- **Cluster detection runs only over watchlisted wallets.**
- **Dedup keys** are deterministic sha256 over a sorted, normalized parts list.
- **Insider language never claims legal insider trading.** UX strings always say "suspicious informed-flow candidate, not a legal insider claim".

See `docs/scoring-strategies.md` and `docs/alert-ux.md` for the full rule set.

## Security / secret handling

- `.env` is git-ignored. **Never commit it.** Only `.env.example` is tracked.
- If you ever copied a real `TELEGRAM_BOT_TOKEN` into `.env` and then any
  process or log surfaced it (chat transcript, CI log, shared screen),
  rotate it immediately in [@BotFather](https://t.me/BotFather)
  (`/revoke` then `/token`). Same for any chat IDs you consider sensitive.
- For staging or dry-runs, set `ALERTING_ENABLED=false`. The router will
  still write `alert_decisions` and `telegram_deliveries` rows
  (`status='skipped'`) so you can audit what *would* have been sent, but
  Telegram is never called.
- The service is read-only against Polymarket — no private keys, no
  trading endpoints. Outbound calls are limited to Gamma/Data API/CLOB
  REST+WS/Next.js (optional) and `api.telegram.org`.
