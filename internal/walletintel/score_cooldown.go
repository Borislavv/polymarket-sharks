package walletintel

import (
	"sync"
	"time"
)

// ScoreCooldown is an in-memory dedup/cooldown gate for ScoreWallet. The
// HolderScanWorker, HistoryBackfillWorker, and other discovery surfaces can
// all trigger scoring for the same wallet within seconds. Without a gate the
// service rewrites wallet_scores 10–50× per wallet per minute with no new
// information.
//
// Semantics:
//   - Allow always when newBet != nil (event-driven scoring, never throttled)
//   - Allow always when score_version mismatches recorded entry
//   - Allow always when entry is older than TTL (default 15m)
//   - Otherwise skip with a recorded reason
//
// The gate is bounded by an LRU-ish eviction: once size exceeds maxEntries,
// the oldest entries are dropped. No persistent state — restarts give every
// wallet a fresh score, which is intentional.
type ScoreCooldown struct {
	mu         sync.Mutex
	entries    map[string]cooldownEntry
	ttl        time.Duration
	maxEntries int
}

type cooldownEntry struct {
	at           time.Time
	scoreVersion string
}

// NewScoreCooldown returns a gate with the given TTL. ttl<=0 disables the gate.
func NewScoreCooldown(ttl time.Duration) *ScoreCooldown {
	return &ScoreCooldown{
		entries:    make(map[string]cooldownEntry),
		ttl:        ttl,
		maxEntries: 20_000,
	}
}

// Allow returns (true, "") if scoring should proceed, or (false, reason) if
// the call is a duplicate inside the cooldown window. force=true bypasses
// the TTL — used by event-driven paths (large-trade capture, new bet,
// backfill-completed, version bump).
func (s *ScoreCooldown) Allow(walletID string, scoreVersion string, force bool) (bool, string) {
	if s == nil || s.ttl <= 0 {
		return true, ""
	}
	if force {
		s.record(walletID, scoreVersion)
		return true, ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[walletID]
	if !ok {
		s.entries[walletID] = cooldownEntry{at: time.Now(), scoreVersion: scoreVersion}
		s.evictIfNeededLocked()
		return true, ""
	}
	if e.scoreVersion != scoreVersion {
		s.entries[walletID] = cooldownEntry{at: time.Now(), scoreVersion: scoreVersion}
		return true, ""
	}
	if time.Since(e.at) >= s.ttl {
		s.entries[walletID] = cooldownEntry{at: time.Now(), scoreVersion: scoreVersion}
		return true, ""
	}
	return false, "scoring_cooldown_active"
}

func (s *ScoreCooldown) record(walletID, scoreVersion string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[walletID] = cooldownEntry{at: time.Now(), scoreVersion: scoreVersion}
	s.evictIfNeededLocked()
}

func (s *ScoreCooldown) evictIfNeededLocked() {
	if len(s.entries) <= s.maxEntries {
		return
	}
	// Drop everything older than 2×TTL; that's the cheapest sweep.
	cutoff := time.Now().Add(-2 * s.ttl)
	for k, v := range s.entries {
		if v.at.Before(cutoff) {
			delete(s.entries, k)
		}
	}
}
