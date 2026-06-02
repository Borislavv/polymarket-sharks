//go:build integration

// Run with:
//
//	INTEGRATION_DB_URL=postgres://watchtower:watchtower@localhost:5547/watchtower?sslmode=disable \
//	go test -tags=integration ./internal/storage/postgres/... -run ClosedPositions
package postgres

import (
	"context"
	"testing"
	"time"
)

func mkClosedRow(walletID, cond, outcome string) ClosedPositionRow {
	return ClosedPositionRow{
		WalletID:    walletID,
		ConditionID: cond,
		Outcome:     outcome,
		TotalBought: 100,
		RealizedPnL: 25,
		AvgPrice:    0.4,
		IsClosed:    true,
		Raw:         []byte(`{"v":1}`),
	}
}

// TestIntegration_ClosedPositions_UpsertIsLatestState is the regression
// test that protects against the bug fixed in migration 0012: repeated
// observations of the same (wallet, condition, outcome) must update one
// row, not append new ones.
func TestIntegration_ClosedPositions_UpsertIsLatestState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wid, err := s.UpsertWallet(ctx, Wallet{ProxyWallet: "0xclosed1"})
	if err != nil {
		t.Fatal(err)
	}
	row := mkClosedRow(wid, "0xCOND", "YES")

	// First observation → insert.
	res, err := s.UpsertClosedPositions(ctx, wid, []ClosedPositionRow{row})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if res.Inserted != 1 || res.Updated != 0 {
		t.Fatalf("first upsert: inserted=%d updated=%d", res.Inserted, res.Updated)
	}

	// Second observation with the same payload → update, not insert.
	res2, err := s.UpsertClosedPositions(ctx, wid, []ClosedPositionRow{row})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Inserted != 0 || res2.Updated != 1 {
		t.Fatalf("second upsert: inserted=%d updated=%d", res2.Inserted, res2.Updated)
	}

	var rowCount int
	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM wallet_closed_position_latest WHERE wallet_id=$1::uuid`, wid).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("expected 1 row after two upserts, got %d", rowCount)
	}
}

func TestIntegration_ClosedPositions_FirstSeenAtStableLastSeenAtAdvances(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wid, _ := s.UpsertWallet(ctx, Wallet{ProxyWallet: "0xclosed2"})
	row := mkClosedRow(wid, "0xCOND", "YES")

	if _, err := s.UpsertClosedPositions(ctx, wid, []ClosedPositionRow{row}); err != nil {
		t.Fatal(err)
	}
	var firstSeen1, lastSeen1 time.Time
	if err := s.Pool.QueryRow(ctx,
		`SELECT first_seen_at, last_seen_at FROM wallet_closed_position_latest WHERE wallet_id=$1::uuid`, wid).
		Scan(&firstSeen1, &lastSeen1); err != nil {
		t.Fatal(err)
	}

	// Force a measurable delta so the second observation's now() differs.
	time.Sleep(15 * time.Millisecond)

	row.RealizedPnL = 30
	if _, err := s.UpsertClosedPositions(ctx, wid, []ClosedPositionRow{row}); err != nil {
		t.Fatal(err)
	}
	var firstSeen2, lastSeen2 time.Time
	var realized float64
	if err := s.Pool.QueryRow(ctx,
		`SELECT first_seen_at, last_seen_at, realized_pnl FROM wallet_closed_position_latest WHERE wallet_id=$1::uuid`, wid).
		Scan(&firstSeen2, &lastSeen2, &realized); err != nil {
		t.Fatal(err)
	}
	if !firstSeen1.Equal(firstSeen2) {
		t.Fatalf("first_seen_at must be stable across updates: %v vs %v", firstSeen1, firstSeen2)
	}
	if !lastSeen2.After(lastSeen1) {
		t.Fatalf("last_seen_at must advance: %v vs %v", lastSeen1, lastSeen2)
	}
	if realized != 30 {
		t.Fatalf("realized_pnl was not updated: %v", realized)
	}
}

func TestIntegration_ClosedPositions_DistinctOutcomesAndWallets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wid1, _ := s.UpsertWallet(ctx, Wallet{ProxyWallet: "0xclosedA"})
	wid2, _ := s.UpsertWallet(ctx, Wallet{ProxyWallet: "0xclosedB"})

	rows := []ClosedPositionRow{
		mkClosedRow(wid1, "0xC1", "YES"),
		mkClosedRow(wid1, "0xC1", "NO"),
		mkClosedRow(wid1, "0xC2", "YES"),
	}
	if _, err := s.UpsertClosedPositions(ctx, wid1, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertClosedPositions(ctx, wid2, []ClosedPositionRow{mkClosedRow(wid2, "0xC1", "YES")}); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM wallet_closed_position_latest WHERE wallet_id=$1::uuid`, wid1).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("wallet1: expected 3 distinct rows, got %d", n)
	}
	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM wallet_closed_position_latest WHERE wallet_id=$1::uuid`, wid2).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("wallet2: expected 1 row, got %d", n)
	}
}

func TestIntegration_ClosedPositions_RepeatedRefreshKeepsRowCountStable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wid, _ := s.UpsertWallet(ctx, Wallet{ProxyWallet: "0xrepeat"})
	batch := []ClosedPositionRow{
		mkClosedRow(wid, "0xR1", "YES"),
		mkClosedRow(wid, "0xR1", "NO"),
		mkClosedRow(wid, "0xR2", "YES"),
		mkClosedRow(wid, "0xR3", "YES"),
	}
	// 10 refresh cycles with identical payload — this is the legacy bug
	// the new design fixes.
	for i := 0; i < 10; i++ {
		if _, err := s.UpsertClosedPositions(ctx, wid, batch); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}
	var n int
	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM wallet_closed_position_latest WHERE wallet_id=$1::uuid`, wid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(batch) {
		t.Fatalf("storage regression: expected %d rows after 10 identical refreshes, got %d", len(batch), n)
	}
}

func TestIntegration_ClosedPositions_TouchUpdatesTimestampsOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wid, _ := s.UpsertWallet(ctx, Wallet{ProxyWallet: "0xtouch"})
	row := mkClosedRow(wid, "0xT", "YES")
	row.RealizedPnL = 11
	if _, err := s.UpsertClosedPositions(ctx, wid, []ClosedPositionRow{row}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(15 * time.Millisecond)
	keys := []ClosedPositionKey{{ConditionID: "0xT", Outcome: "YES"}}
	touched, err := s.TouchClosedPositionsLastSeen(ctx, wid, keys)
	if err != nil {
		t.Fatal(err)
	}
	if touched != 1 {
		t.Fatalf("expected 1 touched row, got %d", touched)
	}

	var realized float64
	var lastSeen, firstSeen time.Time
	if err := s.Pool.QueryRow(ctx,
		`SELECT realized_pnl, first_seen_at, last_seen_at FROM wallet_closed_position_latest WHERE wallet_id=$1::uuid`, wid).
		Scan(&realized, &firstSeen, &lastSeen); err != nil {
		t.Fatal(err)
	}
	if realized != 11 {
		t.Fatalf("touch must not overwrite payload fields: realized_pnl=%v", realized)
	}
	if !lastSeen.After(firstSeen) {
		t.Fatalf("touch must advance last_seen_at: first=%v last=%v", firstSeen, lastSeen)
	}
}

func TestIntegration_ClosedPositions_InsertWithEmptyRawDefaultsToObject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wid, _ := s.UpsertWallet(ctx, Wallet{ProxyWallet: "0xempty"})
	row := mkClosedRow(wid, "0xE", "YES")
	row.Raw = nil
	if _, err := s.UpsertClosedPositions(ctx, wid, []ClosedPositionRow{row}); err != nil {
		t.Fatal(err)
	}
	var rawTxt string
	if err := s.Pool.QueryRow(ctx,
		`SELECT raw::text FROM wallet_closed_position_latest WHERE wallet_id=$1::uuid`, wid).Scan(&rawTxt); err != nil {
		t.Fatal(err)
	}
	if rawTxt != `{}` {
		t.Fatalf("raw was not defaulted to {}: %q", rawTxt)
	}
}
