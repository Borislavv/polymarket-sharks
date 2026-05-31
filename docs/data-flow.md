# Data flow

## Discovery → markets

```
Gamma /events?tag=politics → events row + markets rows + market_tokens row
```

`raw` jsonb is preserved on each row so a future schema change can re-derive without re-fetching.

## Holders → wallets, scores, watchlist

```
Data API /holders?market=<condId>
  → upsert wallets row (proxyWallet pseudonym)
  → insert holder_snapshots row (rank, amount, pct=null if denom unknown)
  → trigger ScoreWallet for top-N holders
       ↓
   AssembleFacts (user-summary, closed-positions, trades)
       ↓
   ScoreShark, ScoreInsiderLike, EvaluateRulesRisk, ScoreArbitrate
       ↓
   InsertScore (audit) + UpsertWatchlist (status: active / watch_only / rejected)
```

## Watched bet → alert

```
WatchedWalletWorker:
  Data API /activity?user=<watched proxy>
      ↓
  if outcome+side missing → /trades enrichment
      ↓
  DirectionLabel(outcome, side) → YES_BUY / YES_SELL / NO_BUY / NO_SELL
      ↓
  InsertWalletTrade (idempotent on tx_hash + identity)
      ↓
  if new → InsertWatchedBet (notional, odds, payoff, snapshot)
      ↓
  AlertRouter.Route (alert_decisions row first, Telegram second)
      ↓
  telegram_deliveries row (ok / failed)
```

## Cluster

```
ClusterWorker every CLUSTER_SCAN_INTERVAL:
  ListRecentWatchedBets(since = now - window - 1h)
      ↓
  walletintel.FindClusters(bets, ClusterParams)
      ↓
  For each cluster:
      UpsertCluster (dedup_key sha256)
      if newly inserted → AlertRouter.Route → CLUSTER channel
```

## News (optional, feature-flagged)

```
NewsWorker (if NEXTJS_NEWS_ENABLED):
  ResolveBuildID from <base>/event/<slug> HTML (cached for TTL)
      ↓
  Fetch <base>/_next/data/<buildId>/en/event/<slug>.json
      ↓
  For each news/timeline/annotation item:
      fingerprint = sha256(slug | title | url | ts)
      InsertNews (idempotent) → AlertRouter NEWS channel
```

`404 → invalidate cached buildId, retry once`. Failures here do not crash the rest of the pipeline.
