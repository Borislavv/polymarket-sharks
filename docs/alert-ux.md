# Alert UX

All user-facing alerts must be readable in 3–5 seconds. No debug dumps. No multi-paragraph essays. Required: notional, price, odds, payoff, links.

## SHARK IS MOVING (channel: `TELEGRAM_BETS_CHAT_ID`)

```
SHARK IS MOVING · WARNING

Trader: <pseudonym or 0xab…cdef>
Class: shark
Quality: <grade> · score <score>/100 · confidence <0.xx>
History: <N> trades · win-rate <x>% · PnL <usd or n/a>
New bet: <YES_BUY/YES_SELL/NO_BUY/NO_SELL> on <market title>
Size: $<notional> @ <price>¢
Odds: x<odds> · Payoff if win: $<payoff>
Why: <2–4 humanized reason codes>
Links: Market · Event · Trader
```

## INSIDER IS IN THE HOUSE (channel: `TELEGRAM_BETS_CHAT_ID`)

```
INSIDER IS IN THE HOUSE · HIGH

Wallet: <0xab…cdef>
Class: insider-like candidate
History: <N> trades · <M> markets
Move: <DIR> on <market title>
Size: $<notional> @ <price>¢
Odds: x<odds> · Payoff if win: $<payoff>
Why: new wallet · unusually large bet · high-impact market · near catalyst
Note: suspicious informed-flow candidate, not a legal insider claim
Links: Market · Event · Trader
```

**Required disclaimer**: the literal sentence "suspicious informed-flow candidate, not a legal insider claim". Tested.

## CLUSTER BET DETECTED (channel: `TELEGRAM_CLUSTERS_CHAT_ID`)

```
CLUSTER BET DETECTED · HIGH

Direction: YES_BUY / YES_SELL / NO_BUY / NO_SELL
Market: <title>
Event: <event title>
Total size: $<total> · Wallets: <n> watched traders
Weighted price: <p>¢ · Odds: x<odds> · Payoff if win: $<payoff>
Window: 6h
Traders: <comma-separated short links>
Why: watched wallets aligned · shark participant · near catalyst
Links: Event · Market
```

## POLYMARKET EVENT NEWS (channel: `TELEGRAM_NEWS_CHAT_ID`)

```
POLYMARKET EVENT NEWS

Event: <title>
News: <headline>
Summary: <short>
Time: <UTC>
Links: Event · Source
```

## Rules

- All alerts are routed by `alerts.Router.Route`. Only `Router` may call Telegram.
- `alert_decisions` is persisted **before** sending. Send failure is recorded in `telegram_deliveries` (status=failed, error=msg).
- Dedup keys are deterministic sha256 over normalized parts; duplicates short-circuit at the decision insert.
- Admin channel may include diagnostics (reason codes, skip reasons); user channels never include debug content.
- If a required link (event/market/trader) is missing, the decision is still persisted; user-facing alert degrades to admin-only with `MISSING_LINK` reason.
