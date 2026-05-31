package walletintel

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// ClosedPositionSnapshotCache keeps the latest known fingerprint per
// (wallet_id, condition_id, outcome) so the backfill worker can skip
// rewriting unchanged rows and limit DB load to genuinely new state.
//
// The cache is the canonical "what we last wrote" record for the running
// process. On restart it is empty; the first refresh after restart pays
// one upsert per position to reseed it. The DB row is still the durable
// source of truth — the cache only avoids redundant writes.
//
// All methods are safe for concurrent use.
type ClosedPositionSnapshotCache struct {
	mu      sync.Mutex
	entries map[closedPositionCacheKey]closedPositionCacheEntry
}

type closedPositionCacheKey struct {
	walletID    string
	conditionID string
	outcome     string
}

type closedPositionCacheEntry struct {
	fingerprint [32]byte
	updatedAt   time.Time
}

// NewClosedPositionSnapshotCache returns an empty cache ready for use.
func NewClosedPositionSnapshotCache() *ClosedPositionSnapshotCache {
	return &ClosedPositionSnapshotCache{
		entries: make(map[closedPositionCacheKey]closedPositionCacheEntry),
	}
}

// Len returns the number of cached entries (used by metrics).
func (c *ClosedPositionSnapshotCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Plan splits a candidate batch into rows whose payload changed (need a
// full upsert) and rows that match the cached fingerprint (need only a
// last_seen_at touch). The cache is not mutated by Plan — call Commit with
// the rows that were actually persisted to advance the fingerprints.
func (c *ClosedPositionSnapshotCache) Plan(walletID string, rows []postgres.ClosedPositionRow) ClosedPositionPlan {
	plan := ClosedPositionPlan{}
	if c == nil || len(rows) == 0 {
		plan.Changed = rows
		return plan
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range rows {
		k := closedPositionCacheKey{walletID: walletID, conditionID: r.ConditionID, outcome: r.Outcome}
		fp := fingerprintClosedPosition(r)
		prev, ok := c.entries[k]
		if ok && prev.fingerprint == fp {
			plan.Unchanged = append(plan.Unchanged, postgres.ClosedPositionKey{
				ConditionID: r.ConditionID,
				Outcome:     r.Outcome,
			})
			continue
		}
		plan.Changed = append(plan.Changed, r)
	}
	return plan
}

// Commit records the fingerprints of rows that were just upserted so a
// subsequent Plan against the same payload can short-circuit. Call this
// only after the DB write succeeded.
func (c *ClosedPositionSnapshotCache) Commit(walletID string, rows []postgres.ClosedPositionRow) {
	if c == nil || len(rows) == 0 {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range rows {
		k := closedPositionCacheKey{walletID: walletID, conditionID: r.ConditionID, outcome: r.Outcome}
		c.entries[k] = closedPositionCacheEntry{
			fingerprint: fingerprintClosedPosition(r),
			updatedAt:   now,
		}
	}
}

// ClosedPositionPlan is the cache's decision for a refresh batch.
//   - Changed: rows whose normalized payload differs from cache → full upsert.
//   - Unchanged: keys whose payload matches cache → last_seen_at heartbeat only.
type ClosedPositionPlan struct {
	Changed   []postgres.ClosedPositionRow
	Unchanged []postgres.ClosedPositionKey
}

// fingerprintClosedPosition produces a stable 32-byte hash over the fields
// the scoring layer reads. Raw jsonb is excluded — it is a duplicate of the
// structured fields and would defeat the "skip unchanged" optimization if
// its key order ever varied. observed_at is excluded by construction
// (always now() per fetch).
func fingerprintClosedPosition(r postgres.ClosedPositionRow) [32]byte {
	h := sha256.New()
	writeString := func(s string) {
		var n [4]byte
		binary.LittleEndian.PutUint32(n[:], uint32(len(s)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(s))
	}
	writeFloat := func(f float64) {
		writeString(strconv.FormatFloat(normalizeFloat(f), 'g', -1, 64))
	}
	writeInt := func(i int) {
		writeString(strconv.Itoa(i))
	}
	writeBool := func(b bool) {
		if b {
			writeString("1")
		} else {
			writeString("0")
		}
	}
	writeString(r.WalletID)
	writeString(r.ConditionID)
	writeString(r.MarketID)
	writeString(r.EventSlug)
	writeString(r.Outcome)
	writeInt(r.OutcomeIndex)
	writeFloat(r.TotalBought)
	writeFloat(r.RealizedPnL)
	writeFloat(r.AvgPrice)
	writeFloat(r.CurrentValue)
	writeFloat(r.PercentPnL)
	writeFloat(r.PercentRealizedPnL)
	writeFloat(r.SizeAtObservation)
	writeBool(r.IsClosed)
	if r.ClosedAt != nil {
		writeString(r.ClosedAt.UTC().Format(time.RFC3339Nano))
	} else {
		writeString("")
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// normalizeFloat collapses -0 → 0 and NaN → 0 so equivalent payloads
// produce identical fingerprints regardless of how the upstream API
// encoded them.
func normalizeFloat(f float64) float64 {
	if math.IsNaN(f) {
		return 0
	}
	if f == 0 {
		return 0
	}
	return f
}
