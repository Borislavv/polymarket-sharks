package walletintel

import (
	"errors"
	"math"
	"testing"
)

func TestDirectionOf(t *testing.T) {
	cases := []struct {
		outcome, side   string
		wantDir         Direction
		wantOutcome     string
		wantCategorical bool
		wantErr         error
	}{
		// Binary YES/NO
		{"YES", "BUY", DirYesBuy, "", false, nil},
		{"yes", "buy", DirYesBuy, "", false, nil},
		{"YES", "SELL", DirYesSell, "", false, nil},
		{"NO", "BUY", DirNoBuy, "", false, nil},
		{"NO", "SELL", DirNoSell, "", false, nil},
		// Categorical / multi-outcome
		{"CHICAGO WHITE SOX", "BUY", DirOutcomeBuy, "CHICAGO WHITE SOX", true, nil},
		{"Miami Marlins", "sell", DirOutcomeSell, "MIAMI MARLINS", true, nil},
		{"UP", "BUY", DirOutcomeBuy, "UP", true, nil},
		{"OVER", "SELL", DirOutcomeSell, "OVER", true, nil},
		{"Paris Saint-Germain FC", "BUY", DirOutcomeBuy, "PARIS SAINT-GERMAIN FC", true, nil},
		// Errors
		{"", "BUY", "", "", false, ErrMissingOutcome},
		{"YES", "", "", "", false, ErrMissingSide},
		{"CHICAGO WHITE SOX", "", "", "", false, ErrMissingSide},
	}
	for _, c := range cases {
		got, err := DirectionOf(c.outcome, c.side)
		if c.wantErr != nil {
			if err == nil {
				t.Fatalf("DirectionOf(%q,%q) expected error %v, got nil", c.outcome, c.side, c.wantErr)
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("DirectionOf(%q,%q) error = %v, want %v", c.outcome, c.side, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("DirectionOf(%q,%q) unexpected error: %v", c.outcome, c.side, err)
		}
		if got.Direction != c.wantDir {
			t.Fatalf("DirectionOf(%q,%q) direction = %q, want %q", c.outcome, c.side, got.Direction, c.wantDir)
		}
		if got.DirectionOutcome != c.wantOutcome {
			t.Fatalf("DirectionOf(%q,%q) directional_outcome = %q, want %q", c.outcome, c.side, got.DirectionOutcome, c.wantOutcome)
		}
		if got.Direction.IsCategorical() != c.wantCategorical {
			t.Fatalf("DirectionOf(%q,%q) IsCategorical() = %v, want %v", c.outcome, c.side, got.Direction.IsCategorical(), c.wantCategorical)
		}
	}
}

func TestDirectionLabel(t *testing.T) {
	cases := []struct {
		outcome, side string
		want          Direction
		err           bool
	}{
		{"YES", "BUY", DirYesBuy, false},
		{"yes", "sell", DirYesSell, false},
		{"NO", "BUY", DirNoBuy, false},
		{"NO", "SELL", DirNoSell, false},
		// categorical multi-outcome markets (team names, city names, etc.)
		{"CHICAGO WHITE SOX", "BUY", "", true},
		{"MIAMI MARLINS", "SELL", "", true},
		// missing fields
		{"MAYBE", "BUY", "", true},
		{"YES", "", "", true},
		{"", "BUY", "", true},
		{"YES", "HOLD", "", true},
	}
	for _, c := range cases {
		got, err := DirectionLabel(c.outcome, c.side)
		if (err != nil) != c.err {
			t.Fatalf("DirectionLabel(%q,%q) err=%v wantErr=%v", c.outcome, c.side, err, c.err)
		}
		if got != c.want {
			t.Fatalf("DirectionLabel(%q,%q) = %q want %q", c.outcome, c.side, got, c.want)
		}
	}
}

func TestOddsAndPayoff(t *testing.T) {
	odds, err := OddsFromPrice(0.25)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if math.Abs(odds-4.0) > 1e-9 {
		t.Fatalf("odds 1/0.25 = %v want 4", odds)
	}
	payoff, err := PayoffIfWin(0.25, 100)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if math.Abs(payoff-400) > 1e-9 {
		t.Fatalf("payoff = %v want 400", payoff)
	}

	if _, err := OddsFromPrice(0); err == nil {
		t.Fatalf("expected error for price 0")
	}
	if _, err := OddsFromPrice(-1); err == nil {
		t.Fatalf("expected error for negative price")
	}
	if _, err := OddsFromPrice(1.5); err == nil {
		t.Fatalf("expected error for price > 1")
	}
	if _, err := PayoffIfWin(0.5, 0); err == nil {
		t.Fatalf("expected error for notional 0")
	}
}
