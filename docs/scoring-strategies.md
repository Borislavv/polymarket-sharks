# Scoring strategies

All strategies are pure Go functions in `internal/walletintel`. Inputs are
assembled into `WalletFacts`; outputs are `ScoreResult` with feature
snapshot, reason codes, missing data, and a `Promote` flag. Every score is
persisted (audit), even when not promoted.

## A. `shark_score`

Goal: find mature, follow-worthy traders.

### Gates
- **Sample size**: `total_trades >= SHARK_MIN_TRADES` OR `closed_positions >= SHARK_MIN_CLOSED_POSITIONS`. Fail → `INSUFFICIENT_SAMPLE`, blocks promotion.
- **PnL sign**: `realized_pnl < -1000` → `NEGATIVE_REALIZED_PNL`, blocks promotion. Unknown → `MISSING_REALIZED_PNL`, lowers confidence.
- **Win-rate anti-abuse**: if `win_rate >= 0.65` but `payoff_quality_score < 5` → `BAD_PAYOFF_PROFILE`, blocks promotion.

### Components (max = 100)

| component | max | rule |
|---|---|---|
| sample_size_score | 15 | linear up to 2× threshold |
| realized_pnl_score | 20 | `(log10(pnl) - 2.5) * 8`, clamped |
| payoff_quality_score | 15 | `(AvgWin/AvgLoss - 0.5) * 6`, clamped |
| clv_proxy_score | 15 | positive-drift trade share × 15 |
| category_focus_score | 10 | `in_target_cat / total_trades × 10` |
| bet_size_discipline_score | 10 | median/p95 sanity + tiny-share penalty |
| win_rate_score | 3 | small contribution; capped |
| recency_score | 5 | linear decay over `SHARK_MAX_STALE_DAYS` |
| holder_quality_score | 5 | rank + pct (if valid) + persistence |
| large_bet_selectivity_score | 2 | (large_bet_winrate - 0.5) × 4 |
| **penalties** | up to **-25** | INSUFFICIENT_SAMPLE, NEGATIVE_REALIZED_PNL, BAD_PAYOFF_PROFILE, TAKER_ONLY_HISTORY, STALE_WALLET |

### Confidence
Starts at 1.0; multiplied by 0.5 (insufficient sample), 0.85 (missing pnl), 0.9 (missing CLV), 0.8 (taker-only), 0.85 (stale).

### Promotion
`score >= SHARK_MIN_SCORE AND confidence >= SHARK_MIN_CONFIDENCE AND no hard-blocking reason`.

### Reason codes (positive)
`HIGH_SAMPLE_SIZE`, `POSITIVE_REALIZED_PNL`, `STRONG_PAYOFF_PROFILE`, `POSITIVE_CLV_PROXY`, `CATEGORY_FOCUSED`, `LARGE_BET_SELECTIVE`, `RECENT_ACTIVITY`, `TOP_HOLDER`.

### Negative
`INSUFFICIENT_SAMPLE`, `NEGATIVE_REALIZED_PNL`, `BAD_PAYOFF_PROFILE`, `MISSING_CLV_DATA`, `MISSING_REALIZED_PNL`, `TAKER_ONLY_HISTORY`, `STALE_WALLET`, `LOW_CONFIDENCE`.

---

## B. `insider_like_score`

Goal: find new/near-new suspicious informed-flow candidates.

**Language rule**: never claim legal insider trading. Reason codes and UX
strings only ever use "suspicious informed-flow candidate".

### Gates
- **Mature-wallet reject**: `total_trades > MaxLifetimeTrades AND markets > MaxLifetimeMarkets` → `MATURE_WALLET_NOT_INSIDER_LIKE`, blocks.
- **Notional gate**: `notional < INSIDER_MIN_NOTIONAL_USD` → `BELOW_NOTIONAL_THRESHOLD`, blocks (unless `SUSPICIOUS_STREAK`).
- **Direction known**: `outcome+side` unknown → `MISSING_TRADE_DIRECTION`, no user alert.
- **Market relevance**: low-impact market → `-5` penalty + confidence reduction.

### Components (max = 100)

| component | max | rule |
|---|---|---|
| new_wallet_score | 20 | 0 trades → 20; piecewise down to 0 over MaxLifetime×2 |
| notional_anomaly_score | 20 | `log2(notional / min)` shifted, clamped |
| low_probability_score | 10 | piecewise on price (≤thr → 10, ≤0.30 → 7, ≤0.40 → 4) |
| payoff_score | 5 | `min(5, profit_if_win / 10000)` |
| holder_concentration_score | 15 | rank + pct (if valid) + sudden concentration |
| catalyst_proximity_score | 10 | known catalyst within window |
| market_impact_score | 10 | high-impact category |
| streak_anomaly_score | 10 | `2 prior wins → 5`, `3+ → 10` |
| **penalties** | up to **-30** | mature wallet, below notional, non-impact market |

### Confidence
1.0 × 0.5 (missing direction) × 0.85 (missing catalyst) × 0.8 (low impact) × 0.2 (mature).

### Promotion
`score >= INSIDER_MIN_SCORE AND confidence >= INSIDER_MIN_CONFIDENCE AND no hard block`.

### Reason codes
`NEW_WALLET`, `LOW_LIFETIME_HISTORY`, `FIRST_LARGE_BET`, `LARGE_NOTIONAL`, `LOW_PROBABILITY_ENTRY`, `HIGH_PAYOFF`, `TOP_HOLDER_POSITION`, `SUDDEN_HOLDER_CONCENTRATION`, `NEAR_CATALYST`, `HIGH_IMPACT_MARKET`, `SUSPICIOUS_STREAK`, `MISSING_TRADE_DIRECTION`, `MISSING_CATALYST_DATA`, `MATURE_WALLET_NOT_INSIDER_LIKE`, `BELOW_NOTIONAL_THRESHOLD`, `RULES_RISK_HIGH`.

---

## C. `lucky_spike_score`

Goal: find suspicious high-frequency profitable traders/bots across the full
Polymarket trade stream. The detector watches the global `/trades` feed,
evaluates candidates as soon as they show recent high-frequency activity, then
pulls wallet history to verify realized profitability.

### Gates
- **Frequency**: strict mode requires the configured full-window count (`5040` trades/week at 2m cadence; monthly fields are diagnostic/secondary). Wallet history is fetched for the weekly lookback from `/activity` with `start`, descending offset pages up to the verified offset cap, and `end=<oldest_ts-1>` continuation; `/trades` is used only as a recent candidate radar. If the local activity page safety cap truncates history, cap-aware observed mode can pass with `LUCKY_SPIKE_MIN_OBSERVED_TRADES`, observed coverage, and observed average interval `<= LUCKY_SPIKE_MAX_AVG_TRADE_INTERVAL`.
- **Coverage**: strict mode requires `LUCKY_SPIKE_MIN_COVERAGE`; observed mode requires `LUCKY_SPIKE_MIN_OBSERVED_COVERAGE`.
- **Profitability**: Polymarket-native `profit_pct = profile_pnl_delta / entry_notional` must be strictly above `LUCKY_SPIKE_MIN_PROFIT_PCT`. `profile_pnl_delta` comes from `user-pnl-api.polymarket.com/user-pnl`; if that endpoint fails, the worker falls back to `positions.cashPnl`. `entry_notional` uses `positions.initialValue` first, then `totalBought * avgPrice` as a fallback.
- **Sample quality**: Polymarket position count in the weekly traded markets is diagnostic only. It affects score/confidence and reason codes, but it is not a hard promotion gate because the business signal is high frequency plus Polymarket-native profit.

### Components (max = 100)

| component | max | rule |
|---|---|---|
| frequency_score | 50 | strict or observed trade-frequency ratio |
| profit_score | 35 | uplift above configured Polymarket position profit_pct threshold |
| sample_score | 15 | Polymarket position sample ratio vs min threshold |
| coverage_bonus | +5 | added when weekly span gate passes |

### Confidence
Built from frequency/sample/coverage ratios and reduced by partial-history signals (`DATA_API_OFFSET_CAP_3000`, local page cap).

### Promotion
Frequency, coverage, and profit hard gates pass AND `score >= LUCKY_SPIKE_MIN_SCORE` AND `confidence >= LUCKY_SPIKE_MIN_CONFIDENCE`.

### Reason codes
`WEEKLY_HIGH_FREQUENCY`, `MONTHLY_HIGH_FREQUENCY`, `WEEKLY_OBSERVED_HIGH_FREQUENCY`, `MONTHLY_OBSERVED_HIGH_FREQUENCY`, `PARTIAL_HISTORY_LOWER_BOUND`, `WEEKLY_SUSTAINED_ACTIVITY`, `OBSERVED_SUSTAINED_ACTIVITY`, `POLYMARKET_POSITION_SAMPLE_OK`, `WINDOW_PROFIT_PCT_ABOVE_30PCT`, `SUSPECTED_LUCK_SPIKE_PATTERN`, `NOT_LEGAL_INSIDER_CLAIM`.

---

## D. `rules_risk_modifier`

Goal: do not overpromote markets with ambiguous resolution.

### Heuristics
- No resolution source / no official source → +25, reason `NO_RESOLUTION_SOURCE`
- Vague phrases (`any time`, `in the news`, `materially`, …) → +8, `VAGUE_PHRASE`
- Subjective wording (`believe`, `reasonable`, `credible`, …) → +6, `SUBJECTIVE_WORDING`
- Social media as source → +10, `SOCIAL_MEDIA_AS_SOURCE`
- Ambiguous deadline (`will X ever happen`, `by end of`) → +12, `AMBIGUOUS_DEADLINE`
- UMA uncertainty flag → +20
- negRisk flag → +10
- Disputed → +30 plus auto-BLOCKING

### Levels
- 0–19 → `LOW` (no penalty)
- 20–44 → `MEDIUM` → confidence × 0.85
- 45–74 → `HIGH` → confidence × 0.6, user alert needs score ≥ 80
- 75+ or disputed → `BLOCKING` → user alert suppressed, admin allowed

---

## E. `score_arbitration`

Goal: single final verdict.

### Rules
- Mature wallet **never** becomes `insider_like` (defensive check on top of insider's own gate).
- New wallet **never** becomes `shark` (sample-size gate enforces).
- Negative-PnL mature wallet is not promoted on large exposure alone.
- Cluster evidence (`HasCluster && ClusterScore >= 70`) raises severity from WARNING to HIGH; never rewrites class.
- `RULES_RISK_BLOCKING` → `UserAlertAllowed=false`, `AdminAlertAllowed=true`, class downgraded to `admin_only`.
- `RULES_RISK_HIGH` → requires `final_score >= 80` for user alert; otherwise class downgraded to `watch_only`.

### Output

```go
type FinalDecision struct {
    FinalClass         string  // "shark" | "insider_like" | "rejected" | "watch_only" | "admin_only"
    FinalScore         int
    FinalConfidence    float64
    FinalSeverity      string  // "INFO" | "WARNING" | "HIGH"
    ReasonCodes        []string
    MissingData        []string
    Promote            bool
    UserAlertAllowed   bool
    AdminAlertAllowed  bool
    FeatureSnapshot    FeatureSnapshot
}
```

---

## Score version

`ScoreVersion = "v1.0.0"` is persisted with each `wallet_scores` row and
each `wallet_watchlist` row. Bump on any change that alters numeric
outputs.

## Missing data

Strategies never silently fill in unknowns. Missing data is always
recorded explicitly:

- `MISSING_REALIZED_PNL`, `MISSING_CLV_DATA`, `MISSING_TRADE_DIRECTION`,
  `MISSING_CATALYST_DATA`, `MISSING_NEW_BET_CONTEXT`.

Each missing flag multiplicatively lowers confidence; severe gaps drop
the row below `MinConfidence` automatically.

## Examples

- 200-trade wallet, +$25k PnL, politics-focused, recent activity, positive
  CLV → score ≈ 85, confidence ≈ 0.85, promoted as `shark`.
- 0-trade wallet, $25k on YES_BUY at $0.15 in war market, top holder,
  near catalyst → score ≈ 90, confidence ≈ 0.8, promoted as `insider_like`.
- 200-trade mature wallet placing a $25k bet → insider strategy rejects
  with `MATURE_WALLET_NOT_INSIDER_LIKE`; shark strategy evaluates on its
  own merits.
- Market with no resolution source + subjective wording → `HIGH` rules
  risk, confidence × 0.6, user alert requires score ≥ 80.
