# internal/polymarket

Thin HTTP/WS clients for each Polymarket surface.

## Submodules

| dir | role | source of truth for |
|---|---|---|
| `gamma` | Gamma API | event/market discovery, tags |
| `dataapi` | Data API | **wallet identity, side, direction**, holders, positions, summaries |
| `clob` | CLOB REST + WS | book, midpoint, hotset freshness |
| `nextjs` | Next.js internal | news/timeline annotations (OPTIONAL, feature-flagged) |

## Shared HTTP plumbing

`polymarket.HTTPClient` is the per-surface retrying client. Each surface
gets its own `rate.Limiter`. Retries fire on 429 and 5xx with exponential
backoff (100ms → 8s, max 3 tries). 4xx (other than 429) is non-retryable.

Context is honored everywhere — no goroutine spins on repeated failures.

## Invariants

- Each surface has its own rate limiter; there is no global limiter.
- All requests carry a context with timeout (`HTTP_TIMEOUT`, default 20s).
- Direction (`outcome+side`) is **only** read from Data API `/trades` and
  enriched into `/activity`. CLOB WS is never used to label wallet side.
- WS subscription is dynamic; hotset turnover does not require a
  reconnect.

## How to test

```bash
go test ./internal/polymarket/...
```

Tests use `httptest.Server` fixtures — never real Polymarket endpoints —
and cover: payload parsing, retry on 429, no-retry on 400, context
timeout propagation, malformed JSON, and Next.js buildId caching/refresh.
