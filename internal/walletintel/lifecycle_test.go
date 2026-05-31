package walletintel

import "testing"

func TestIsExitSide(t *testing.T) {
	cases := []struct {
		dir  Direction
		side string
		want bool
	}{
		{DirYesBuy, "SELL", true},
		{DirYesBuy, "BUY", false},
		{DirNoBuy, "SELL", true},
		{DirNoBuy, "BUY", false},
		{DirYesSell, "BUY", true},
		{DirNoSell, "BUY", true},
		{DirYesBuy, "hold", false},
		{DirYesBuy, "", false},
	}
	for _, c := range cases {
		if got := IsExitSide(c.dir, c.side); got != c.want {
			t.Fatalf("%v/%q -> %v want %v", c.dir, c.side, got, c.want)
		}
	}
}

func TestExitPnL_LongPositive(t *testing.T) {
	pnl, known := ExitPnL(DirYesBuy, 0.30, 0.45, 1000)
	if !known {
		t.Fatalf("expected known")
	}
	// float math: (0.45-0.30)*1000 ≈ 150 with sub-eps drift
	if pnl < 149.99 || pnl > 150.01 {
		t.Fatalf("expected ~150, got %v", pnl)
	}
}

func TestExitPnL_MissingFields(t *testing.T) {
	if _, known := ExitPnL(DirYesBuy, 0, 0.5, 100); known {
		t.Fatalf("expected unknown when entry price missing")
	}
	if _, known := ExitPnL(DirYesBuy, 0.3, 0, 100); known {
		t.Fatalf("expected unknown when exit price missing")
	}
	if _, known := ExitPnL(DirYesBuy, 0.3, 0.5, 0); known {
		t.Fatalf("expected unknown when size missing")
	}
}
