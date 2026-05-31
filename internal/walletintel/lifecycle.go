package walletintel

import "strings"

// IsExitSide returns true if `side` against the opening direction looks
// like an exit/reduce (POC semantics: SELL after BUY closes long YES/NO).
//
// Polymarket positions are long-only in current public surfaces, so we
// treat:
//   - YES_BUY → exit is YES_SELL
//   - NO_BUY  → exit is NO_SELL
//   - YES_SELL / NO_SELL openings are also supported by treating the
//     opposite BUY as exit, but real Polymarket flow rarely emits these
//     (most retail sells reduce existing longs).
func IsExitSide(openedDir Direction, side string) bool {
	s := strings.ToUpper(strings.TrimSpace(side))
	switch openedDir {
	case DirYesBuy:
		return s == "SELL"
	case DirNoBuy:
		return s == "SELL"
	case DirYesSell:
		return s == "BUY"
	case DirNoSell:
		return s == "BUY"
	}
	return false
}

// ExitPnL estimates realized PnL for the exited slice (long-only model).
// Returns (pnl, known). Unknown if any price/size missing.
func ExitPnL(openedDir Direction, openPrice, exitPrice, exitedSize float64) (float64, bool) {
	if openPrice <= 0 || exitPrice <= 0 || exitedSize <= 0 {
		return 0, false
	}
	switch openedDir {
	case DirYesBuy, DirNoBuy:
		// long: profit when exit > entry
		return (exitPrice - openPrice) * exitedSize, true
	case DirYesSell, DirNoSell:
		// short-ish: profit when exit < entry (POC best-effort)
		return (openPrice - exitPrice) * exitedSize, true
	}
	return 0, false
}
