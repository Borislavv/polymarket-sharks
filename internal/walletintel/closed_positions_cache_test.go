package walletintel

import (
	"sync"
	"testing"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

func samplePosition(wallet, cond, outcome string, pnl float64) postgres.ClosedPositionRow {
	return postgres.ClosedPositionRow{
		WalletID:    wallet,
		ConditionID: cond,
		Outcome:     outcome,
		TotalBought: 100,
		RealizedPnL: pnl,
		AvgPrice:    0.42,
		IsClosed:    true,
	}
}

func TestSnapshotCache_FirstPlanReportsAllRowsChanged(t *testing.T) {
	c := NewClosedPositionSnapshotCache()
	rows := []postgres.ClosedPositionRow{
		samplePosition("w1", "c1", "YES", 5),
		samplePosition("w1", "c2", "NO", -3),
	}
	plan := c.Plan("w1", rows)
	if len(plan.Changed) != 2 || len(plan.Unchanged) != 0 {
		t.Fatalf("first plan: changed=%d unchanged=%d", len(plan.Changed), len(plan.Unchanged))
	}
}

func TestSnapshotCache_RepeatedIdenticalPayloadIsSkipped(t *testing.T) {
	c := NewClosedPositionSnapshotCache()
	rows := []postgres.ClosedPositionRow{samplePosition("w1", "c1", "YES", 5)}
	plan1 := c.Plan("w1", rows)
	c.Commit("w1", plan1.Changed)
	plan2 := c.Plan("w1", rows)
	if len(plan2.Changed) != 0 {
		t.Fatalf("expected unchanged on second plan, got %d changed", len(plan2.Changed))
	}
	if len(plan2.Unchanged) != 1 {
		t.Fatalf("expected 1 unchanged key, got %d", len(plan2.Unchanged))
	}
	if plan2.Unchanged[0].ConditionID != "c1" || plan2.Unchanged[0].Outcome != "YES" {
		t.Fatalf("unchanged key mismatch: %+v", plan2.Unchanged[0])
	}
}

func TestSnapshotCache_ChangedFieldsTriggerUpsert(t *testing.T) {
	c := NewClosedPositionSnapshotCache()
	first := samplePosition("w1", "c1", "YES", 5)
	c.Commit("w1", []postgres.ClosedPositionRow{first})

	changed := first
	changed.RealizedPnL = 7
	plan := c.Plan("w1", []postgres.ClosedPositionRow{changed})
	if len(plan.Changed) != 1 || len(plan.Unchanged) != 0 {
		t.Fatalf("expected 1 changed row, got changed=%d unchanged=%d", len(plan.Changed), len(plan.Unchanged))
	}
}

func TestSnapshotCache_ClosedAtChangeFlipsFingerprint(t *testing.T) {
	c := NewClosedPositionSnapshotCache()
	first := samplePosition("w1", "c1", "YES", 5)
	c.Commit("w1", []postgres.ClosedPositionRow{first})

	when := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withClosedAt := first
	withClosedAt.ClosedAt = &when
	plan := c.Plan("w1", []postgres.ClosedPositionRow{withClosedAt})
	if len(plan.Changed) != 1 {
		t.Fatalf("expected closed_at addition to change fingerprint, got changed=%d", len(plan.Changed))
	}
}

func TestSnapshotCache_DifferentWalletsDoNotCollide(t *testing.T) {
	c := NewClosedPositionSnapshotCache()
	row := samplePosition("w1", "c1", "YES", 5)
	c.Commit("w1", []postgres.ClosedPositionRow{row})
	// Same condition/outcome but for a different wallet must NOT be cached as unchanged.
	plan := c.Plan("w2", []postgres.ClosedPositionRow{samplePosition("w2", "c1", "YES", 5)})
	if len(plan.Changed) != 1 {
		t.Fatalf("expected wallet isolation, got changed=%d", len(plan.Changed))
	}
}

func TestSnapshotCache_NilReceiverTreatsEverythingAsChanged(t *testing.T) {
	var c *ClosedPositionSnapshotCache // nil
	rows := []postgres.ClosedPositionRow{samplePosition("w1", "c1", "YES", 5)}
	plan := c.Plan("w1", rows)
	if len(plan.Changed) != 1 {
		t.Fatalf("nil cache must pass rows through unchanged, got changed=%d", len(plan.Changed))
	}
	// Commit on nil must not panic.
	c.Commit("w1", rows)
}

func TestSnapshotCache_ConcurrentCommitsAreSafe(t *testing.T) {
	c := NewClosedPositionSnapshotCache()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			row := samplePosition("w1", "c", "YES", float64(i))
			c.Commit("w1", []postgres.ClosedPositionRow{row})
		}(i)
	}
	wg.Wait()
	if c.Len() != 1 {
		t.Fatalf("expected 1 entry for single key after concurrent commits, got %d", c.Len())
	}
}

func TestSnapshotCache_LenTracksEntries(t *testing.T) {
	c := NewClosedPositionSnapshotCache()
	if got := c.Len(); got != 0 {
		t.Fatalf("empty cache Len=%d", got)
	}
	c.Commit("w1", []postgres.ClosedPositionRow{
		samplePosition("w1", "c1", "YES", 1),
		samplePosition("w1", "c1", "NO", 2),
		samplePosition("w1", "c2", "YES", 3),
	})
	if got := c.Len(); got != 3 {
		t.Fatalf("Len after 3 commits = %d", got)
	}
	// Recommit the same keys — entry count must not grow.
	c.Commit("w1", []postgres.ClosedPositionRow{
		samplePosition("w1", "c1", "YES", 99),
	})
	if got := c.Len(); got != 3 {
		t.Fatalf("Len after recommit = %d", got)
	}
}
