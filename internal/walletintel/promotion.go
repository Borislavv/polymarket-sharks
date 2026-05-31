package walletintel

import "time"

// WatchlistPromotion is the persisted view of an arbitrated decision when
// the final class is shark / insider_like / watch_only / admin_only.
type WatchlistPromotion struct {
	WalletID        string
	Class           string
	Status          string
	Score           int
	Confidence      float64
	ReasonCodes     []string
	FeatureSnapshot FeatureSnapshot
	ScoreVersion    string
	PromotedAt      time.Time
}

// Statuses persisted in wallet_watchlist.status. The schema does not enforce
// these via CHECK so new statuses can be added forward-compatibly; values
// declared here are the authoritative set used by application code.
const (
	StatusActive       = "active"
	StatusWatchOnly    = "watch_only"
	StatusRejected     = "rejected"
	StatusAdminOnly    = "admin_only"
	StatusNeedsHistory = "needs_history"
	StatusUnactual     = "unactual"
	StatusStreakBroken = "streak_broken"
	StatusStale        = "stale"
)

// BuildPromotion translates a FinalDecision and supporting facts into a
// watchlist row. Even rejected wallets are stored as 'rejected' for audit.
//
// Status mapping:
//   - shark with hard gates pass    → 'active'
//   - shark with incomplete history → 'needs_history' (no user alerts)
//   - insider_like, clean streak    → 'active'
//   - insider_like, loss observed   → 'streak_broken'
//   - shark previously promoted, now failing → caller invokes reconcile
//     which writes 'unactual'.
func BuildPromotion(walletID string, d FinalDecision, f WalletFacts, now time.Time) WatchlistPromotion {
	status := StatusRejected
	class := d.FinalClass
	switch d.FinalClass {
	case "shark":
		if d.Promote {
			status = StatusActive
		} else if f.ClosedPositionsCountHist > 0 && !f.ClosedPositionsComplete {
			status = StatusNeedsHistory
		}
	case "insider_like":
		if f.LifetimeLosingCount > 0 {
			status = StatusStreakBroken
		} else if d.Promote {
			status = StatusActive
		} else {
			status = StatusWatchOnly
		}
	case "watch_only":
		status = StatusWatchOnly
	case "admin_only":
		status = StatusAdminOnly
	}
	return WatchlistPromotion{
		WalletID:        walletID,
		Class:           class,
		Status:          status,
		Score:           d.FinalScore,
		Confidence:      d.FinalConfidence,
		ReasonCodes:     d.ReasonCodes,
		FeatureSnapshot: d.FeatureSnapshot,
		ScoreVersion:    ScoreVersion,
		PromotedAt:      now,
	}
}
