# internal/walletintel

## What this module does

- Hosts the four deterministic scoring strategies (`shark_score`, `insider_like_score`, `rules_risk_modifier`, `score_arbitration`).
- Defines `WalletFacts` (the input bundle) and `ScoreResult` / `FinalDecision` (the outputs).
- Implements watchlist promotion (`BuildPromotion` + storage adapter).
- Implements cluster detection (`FindClusters`) over watched bets.
- Hosts the live workers: `Runner` (assembles facts + runs scoring), `WatchedWalletWorker`, `ClusterWorker`.

## Reads from

- Data API: `/user-summary`, `/closed-positions`, `/trades`, `/activity`.
- Postgres: `markets`, `events` (for category lookup), `watched_bets` (cluster scan), `wallet_watchlist` (watched wallets).

## Writes to

- `wallets` (upsert on encounter)
- `wallet_scores` (per-strategy result, audit row even when not promoted)
- `wallet_watchlist` (final arbitrated promotion or rejection)
- `wallet_trades`, `watched_bets` (from `WatchedWalletWorker`)
- `bet_clusters` (from `ClusterWorker`)
- `alert_decisions`, `telegram_deliveries` (via `alerts.Router`)

## Invariants

- Scoring functions are **pure** — no IO, no globals.
- `FeatureSnapshot` is JSON-serializable and always populated.
- `ReasonCodes` is non-empty when promote=true.
- `ScoreVersion` constant is persisted on every row.
- Cluster detection runs **only** over watchlisted wallets (input slice is sourced from `wallet_watchlist` joined to `watched_bets`).
- Direction labels come from Data API trades, never CLOB WS.

## Failure modes

- Missing Data API endpoint → score is still produced; `MISSING_*` codes are added; confidence is degraded; promotion may be blocked.
- Direction unknown for an activity row → no `watched_bets` row created (logged + counter `watched_unknown_direction_total`).
- DB upsert failure during promotion → logged warning; the score row is still inserted (audit survives).

## Metrics

`wt_scoring_runs_total`, `wt_watchlist_promotions_total`, `wt_watched_trades_inserted_total`, `wt_watched_bets_inserted_total`, `wt_alerts_shark_sent_total`, `wt_alerts_insider_sent_total`, `wt_alerts_cluster_sent_total`, `wt_clusters_new_total`.

## How to test

```bash
go test ./internal/walletintel/...
```

Tests cover direction labelling, odds/payoff math, all four strategies' gates and promotion paths, and cluster detection (window, direction, wallet count, notional, dedup stability).
