// Package walletintel hosts deterministic scoring strategies, watchlist
// promotion, cluster detection, and pure helpers (direction/odds/payoff).
//
// All scoring functions are pure: inputs → ScoreResult. External fetching
// (Data API, holders, CLV samples) lives in marketscan/workers and is
// assembled into WalletFacts before scoring runs.
package walletintel

import (
	"errors"
	"fmt"
	"strings"
)

// ScoreVersion is bumped on any change that alters score outputs.
// Persisted alongside every wallet_scores row.
//
// v6.0.0 pivots strategy qualification to high-frequency + profitability
// windows (7d/30d) and removes win-rate as a promotion gate. Profitability
// is measured as realized_pnl / entry_notional over the window.
//
// v6.1.0 makes lucky-spike cap-aware for Polymarket Data API history limits
// and streams candidates from the global trades feed.
//
// v6.2.0 makes lucky-spike profitability Polymarket-native: promotion profit
// uses /positions cashPnl divided by initial stake for markets traded in the
// weekly/monthly window, not reconstructed trade cycles.
//
// v6.3.0 paginates /positions and prefers initialValue for entry notional.
//
// v6.4.0 fetches lucky-spike weekly wallet trade history from /activity with
// end-cursor continuation past the verified historical offset cap.
//
// v6.5.0 removes the Polymarket position/cycle sample from lucky-spike hard
// promotion gates; the strategy gates on frequency, coverage, and profit.
//
// v6.6.0 uses Polymarket profile P&L delta as the lucky-spike profit numerator
// when available, falling back to positions cashPnl only if profile P&L fails.
const ScoreVersion = "v6.6.0"

// Direction labels — canonical names used across the pipeline.
type Direction string

const (
	DirYesBuy      Direction = "YES_BUY"
	DirYesSell     Direction = "YES_SELL"
	DirNoBuy       Direction = "NO_BUY"
	DirNoSell      Direction = "NO_SELL"
	DirOutcomeBuy  Direction = "OUTCOME_BUY"
	DirOutcomeSell Direction = "OUTCOME_SELL"
)

func (d Direction) String() string { return string(d) }

// IsCategorical reports whether d is a multi-outcome (non-binary) direction.
func (d Direction) IsCategorical() bool {
	return d == DirOutcomeBuy || d == DirOutcomeSell
}

// ErrMissingSide is returned when side cannot be determined.
var ErrMissingSide = errors.New("walletintel: side is missing or blank")

// ErrMissingOutcome is returned when outcome cannot be determined.
var ErrMissingOutcome = errors.New("walletintel: outcome is missing or blank")

// ErrUnknownDirection is kept for backwards-compatibility with callers that
// only need to know "something went wrong"; prefer the specific errors above.
var ErrUnknownDirection = errors.New("walletintel: unknown direction (outcome/side)")

// DirectionResult carries the full directional context for a trade.
type DirectionResult struct {
	// Direction is the canonical label (YES_BUY, NO_SELL, OUTCOME_BUY, …).
	Direction Direction
	// DirectionOutcome is the raw outcome label for categorical trades
	// (e.g. "CHICAGO WHITE SOX", "UP", "OVER"). Empty for binary YES/NO.
	DirectionOutcome string
}

// DirectionOf normalises a Data API trade's outcome+side to the canonical
// label set. Truth source for direction is Data API trades, never CLOB WS.
//
// Binary (YES/NO):
//
//	YES + BUY  → YES_BUY  (DirectionOutcome = "")
//	YES + SELL → YES_SELL
//	NO  + BUY  → NO_BUY
//	NO  + SELL → NO_SELL
//
// Categorical / multi-outcome (any other outcome string):
//
//	<outcome> + BUY  → OUTCOME_BUY  (DirectionOutcome = normalised outcome)
//	<outcome> + SELL → OUTCOME_SELL
//
// Errors:
//
//	ErrMissingOutcome – outcome is blank after trimming
//	ErrMissingSide    – side is blank after trimming (outcome may be present)
func DirectionOf(outcome, side string) (DirectionResult, error) {
	o := strings.ToUpper(strings.TrimSpace(outcome))
	s := strings.ToUpper(strings.TrimSpace(side))

	if o == "" {
		return DirectionResult{}, ErrMissingOutcome
	}
	if s == "" {
		return DirectionResult{}, ErrMissingSide
	}

	switch o {
	case "YES":
		switch s {
		case "BUY":
			return DirectionResult{Direction: DirYesBuy}, nil
		case "SELL":
			return DirectionResult{Direction: DirYesSell}, nil
		}
	case "NO":
		switch s {
		case "BUY":
			return DirectionResult{Direction: DirNoBuy}, nil
		case "SELL":
			return DirectionResult{Direction: DirNoSell}, nil
		}
	default:
		// Categorical / multi-outcome market.
		switch s {
		case "BUY":
			return DirectionResult{Direction: DirOutcomeBuy, DirectionOutcome: o}, nil
		case "SELL":
			return DirectionResult{Direction: DirOutcomeSell, DirectionOutcome: o}, nil
		}
	}
	return DirectionResult{}, ErrUnknownDirection
}

// DirectionLabel is kept for code that has not yet been updated to
// DirectionOf. It returns error for any non-YES/NO outcome (old behaviour),
// so existing call sites that guard categorical trades stay unchanged until
// they are explicitly migrated to DirectionOf.
//
// Deprecated: use DirectionOf which supports categorical outcomes.
func DirectionLabel(outcome, side string) (Direction, error) {
	o := strings.ToUpper(strings.TrimSpace(outcome))
	s := strings.ToUpper(strings.TrimSpace(side))
	if o == "" || s == "" {
		return "", ErrUnknownDirection
	}
	switch o {
	case "YES":
		switch s {
		case "BUY":
			return DirYesBuy, nil
		case "SELL":
			return DirYesSell, nil
		}
	case "NO":
		switch s {
		case "BUY":
			return DirNoBuy, nil
		case "SELL":
			return DirNoSell, nil
		}
	}
	return "", ErrUnknownDirection
}

// OddsFromPrice returns 1/price for a BUY-side exposure. Polymarket prices
// are in (0, 1]. Invalid prices return an error rather than silent values.
func OddsFromPrice(price float64) (float64, error) {
	if price <= 0 || price > 1 {
		return 0, fmt.Errorf("walletintel: invalid price %v (must be in (0,1])", price)
	}
	return 1.0 / price, nil
}

// PayoffIfWin returns total payoff for `notional` USDC at `price`.
// price 0.25, notional 100 → payoff 400. Profit if win = payoff - notional.
func PayoffIfWin(price, notional float64) (float64, error) {
	if notional <= 0 {
		return 0, fmt.Errorf("walletintel: invalid notional %v", notional)
	}
	o, err := OddsFromPrice(price)
	if err != nil {
		return 0, err
	}
	return notional * o, nil
}
