# internal/alerts

## What this module does

- Defines alert payload types: `SharkBet`, `InsiderBet`, `ClusterAlert`, `NewsAlert`.
- Provides deterministic MarkdownV2 formatters for each.
- Implements `Router`, the centralized component that persists `alert_decisions` and dispatches Telegram messages.
- Hosts `DedupKey(parts...)` — sha256 over a normalized parts list.

## Reads from

- The caller (workers) provides decision + body + channel.

## Writes to

- `alert_decisions` (idempotent on `dedup_key`)
- `telegram_deliveries` (one row per send attempt, `status=ok|failed|skipped`)
- Telegram Bot API via `internal/telegram` (rate-limited)

## Invariants

- **Only `Router` calls Telegram.** No worker may bypass.
- `alert_decisions` is inserted **before** the Telegram call; on send
  failure the decision persists for retry/audit.
- Duplicate dedup keys short-circuit at decision insert — Telegram is
  never re-hit for the same alert.
- `UserAlertAllowed=false` + `AdminAlertAllowed=true` falls back to the
  admin channel.
- Insider message **must** contain the literal sentence
  `"suspicious informed-flow candidate, not a legal insider claim"`
  (covered by `TestFormatInsiderBet_LegalLanguage`).

## Failure modes

- Telegram 4xx/5xx → delivery row with `status=failed, error=<msg>`,
  `Outcome.Err` returned to caller; decision row is preserved.
- Missing chat ID for a channel → `Outcome.SkipReason="no_chat_configured"`.

## Metrics

`wt_alerts_shark_sent_total`, `wt_alerts_insider_sent_total`,
`wt_alerts_cluster_sent_total`.

## How to test

```bash
go test ./internal/alerts/...
```

Tests cover formatter output (required fields, links, no debug content)
and the insider legal-language guard.
