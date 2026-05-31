package alerts

import "testing"

func TestUSDFormatter(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "$0"},
		{0.42, "$0.42"},
		{1.23, "$1.23"},
		{12, "$12.00"},
		{99.5, "$99.50"},
		{100, "$100"},
		{1234, "$1.2k"},
		{12500, "$12.5k"},
		{1_000_000, "$1.00M"},
		{-2500, "$-2.5k"},
	}
	for _, c := range cases {
		if got := usd(c.in); got != c.want {
			t.Fatalf("usd(%v) = %q want %q", c.in, got, c.want)
		}
	}
}
