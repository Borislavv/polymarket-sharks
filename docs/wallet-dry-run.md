# Wallet dry-run

`cmd/cli wallet-dry-run` is a one-shot inspector for a single proxy wallet. It
hits the live Polymarket Data API, backfills the wallet's `/trades` and closed
positions into Postgres, then computes the v4 historical-shark and insider-like
scores against the freshly-loaded evidence. No alerts are sent. The report
prints to stdout.

Use it to:

- validate that a specific wallet would be promoted under the current v4
  thresholds before deploying configuration changes;
- triage a wallet a user has flagged in the bets channel;
- compare two wallets side-by-side.

## Build

```bash
go build -o bin/cli ./cmd/cli
```

## Run

```bash
DATABASE_URL=postgres://watchtower:watchtower@localhost:5547/watchtower?sslmode=disable \
bin/cli wallet-dry-run --wallet 0x5d189e816b4149be00977c1a3c8840374aec4972
```

Optional flags:

| Flag         | Purpose                                                       |
|--------------|---------------------------------------------------------------|
| `--wallet`   | Required. Proxy wallet address (`0x…`).                       |
| `--dsn`      | Override `DATABASE_URL` from the environment.                 |
| `--data-api` | Override `POLYMARKET_DATA_API_BASE_URL` (e.g. for a fixture). |

The CLI honours `SHARK_HIST_MIN_*` and `INSIDER_*` env vars exactly as the
watchtower binary does. When run without `TELEGRAM_BOT_TOKEN` and the other
Telegram secrets, the CLI falls back to env-only defaults (it never sends
Telegram messages).

## Output

```
--- wallet dry-run report ---
wallet              : 0x5d189e816b4149be00977c1a3c8840374aec4972
wallet_id           : 8f...uuid
backfill_duration   : 7.412s
trades_fetched      : 1240 (complete=true)
closed_pos_fetched  : 87 (complete=true)
backfill_error      :

== historical close stats ==
closed_positions    : 87 (profitable=71, losing=16)
win_rate            : 0.8161
ROI                 : 0.4123
avg_closed_stake    : 27412.55
median_closed_stake : 18000.00
realized_pnl        : 985432.10
max_win/max_loss    : 152480.00 / -38120.55
last_closed_at      : 2026-05-18T15:42:11Z

== insider lifetime ==
lifetime_trades     : 1240
streak_clean        : false (wins=71 losses=16)
last_trade_at       : 2026-05-24T11:18:02Z

== shark_score ==
strategy            : shark_score
class               : shark
score               : 84
confidence          : 0.85
promote             : true
score_version       : v4.0.0
reason_codes        : [SHARK_HISTORICAL_EDGE CLOSED_POSITIONS_SAMPLE_OK ROI_ABOVE_33 WIN_RATE_ABOVE_75 AVG_STAKE_ABOVE_10K POSITIVE_REALIZED_PNL]
feature_snapshot    : {...}

== insider_like_score ==
...

== alerting ==
shark_alert_would_send   : true
insider_alert_would_send : false
```

## SQL audit queries

After deploying the v4 refactor, audit the resulting state with these queries
(run via `docker exec polymarket-sharks-postgres-1 psql -U watchtower -d watchtower -c "…"`).

```sql
-- 1. Watchlist class/status breakdown.
select class, status, score_version, count(*)
from wallet_watchlist
group by class, status, score_version
order by count(*) desc;

-- 2. Top active sharks and their evidence.
select
  w.proxy_wallet,
  ww.class,
  ww.status,
  ww.score,
  ww.confidence,
  ww.feature_snapshot->>'promotion_path'           as path,
  ww.feature_snapshot->>'closed_positions_count'   as closed_count,
  ww.feature_snapshot->>'win_rate'                 as win_rate,
  ww.feature_snapshot->>'roi'                      as roi,
  ww.feature_snapshot->>'avg_closed_position_stake' as avg_stake,
  ww.reason_codes
from wallet_watchlist ww
join wallets w on w.id = ww.wallet_id
where ww.status in ('active','watch_only')
order by ww.score desc
limit 50;

-- 3. v4 score distribution.
select strategy, class, promote, reason_codes, count(*)
from wallet_scores
where score_version = 'v4.0.0'
group by strategy, class, promote, reason_codes
order by count(*) desc
limit 50;

-- 4. Hard-gate intersection across all v4 scores.
select
  count(*) filter (where (feature_snapshot->>'closed_positions_count')::numeric >= 25)   as closed_25,
  count(*) filter (where (feature_snapshot->>'roi')::numeric >= 0.33)                    as roi_33,
  count(*) filter (where (feature_snapshot->>'win_rate')::numeric > 0.75)                as wr_75,
  count(*) filter (where (feature_snapshot->>'avg_closed_position_stake')::numeric > 10000) as avg_10k
from wallet_scores
where score_version = 'v4.0.0' and strategy = 'shark_score';

-- 5. Insider-like candidates.
select count(*) as insider_candidates
from wallet_scores
where score_version = 'v4.0.0' and strategy = 'insider_like_score' and class = 'insider_like';

-- 6. Backfill bookkeeping.
select
  count(*) total,
  count(*) filter (where closed_positions_complete) complete,
  count(*) filter (where last_error <> '') errored
from wallet_history_backfills;
```
