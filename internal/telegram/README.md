# internal/telegram

Tiny Bot-API client used exclusively by `alerts.Router`.

## What this module does

- `Client.SendMessage(ctx, chatID, text)` → POST `/bot<token>/sendMessage`
  with `parse_mode=MarkdownV2` and `disable_web_page_preview=true`.
  Returns the Telegram message id.
- `EscapeMD(s)` — escapes all MarkdownV2 reserved characters.

## Invariants

- Per-instance `rate.Limiter` (configurable via `TELEGRAM_RPS_LIMIT`,
  default 1 rps with burst 5).
- All requests have a context timeout (`HTTP_TIMEOUT`).
- No worker imports this package directly; only `alerts.Router` does.

## Failure modes

- HTTP non-2xx → returns a typed error containing status code and body
  excerpt. Router records `status=failed` in `telegram_deliveries`.

## How to test

Manual: against a real Bot token in a test chat. Unit tests cover the
escape function indirectly via `alerts/format_test.go` (which checks that
escaped output still contains expected substrings after de-escaping).
