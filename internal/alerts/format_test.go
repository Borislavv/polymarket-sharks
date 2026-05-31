package alerts

import (
	"fmt"
	"strings"
	"testing"
)

func TestFormatSharkBetContainsRequiredFields(t *testing.T) {
	links := DefaultLinks()
	out := FormatSharkBet(SharkBet{
		WalletShort:          "0xabc123def456",
		WalletFull:           "0xabc123def4560000000000000000000000abcdef",
		Pseudonym:            "PolyShark",
		ProfileSlug:          "polyshark",
		Class:                "shark",
		Score:                82,
		Confidence:           0.78,
		TotalTrades:          220,
		WinRate:              0.62,
		RealizedPnL:          45000,
		RealizedKnown:        true,
		ClosedPositionsCount: 42,
		HistoricalWinRate:    0.81,
		HistoricalROI:        0.45,
		AvgClosedStake:       18500,
		HistoricalPnL:        45000,
		HistoricalPnLKnown:   true,
		PromotionPath:        "historical_shark",
		MarketSlug:           "us-election-2026",
		MarketTitle:          "Will X win 2026?",
		EventSlug:            "us-election",
		Direction:            "YES_BUY",
		Notional:             12000,
		Price:                0.42,
		Odds:                 2.38,
		Payoff:               28560,
		ReasonHumanized:      "deep history · positive realized PnL",
	}, links)
	mustContain(t, out, "SHARK IS MOVING")
	mustContain(t, out, "PolyShark")
	mustContain(t, out, "82/100")
	mustContain(t, out, "42 positions")
	mustContain(t, out, "ROI 45%")
	mustContain(t, out, "historical shark")
	mustContain(t, out, "YES_BUY")
	mustContain(t, out, "Will X win 2026?")
	mustContain(t, out, "polymarket.com/market/us-election-2026")
	mustContain(t, out, "polymarket.com/event/us-election")
	mustContain(t, out, "polymarket.com/profile/polyshark")
	mustContain(t, out, "x2.38")
	// no debug dumps
	if strings.Contains(out, "feature_snapshot") || strings.Contains(out, "DEBUG") {
		t.Fatalf("user-facing message contains debug content:\n%s", out)
	}
}

func TestFormatInsiderBet_LegalLanguage(t *testing.T) {
	links := DefaultLinks()
	out := FormatInsiderBet(InsiderBet{
		WalletShort:     "0x0123456789abcdef",
		LifetimeTrades:  1,
		LifetimeMarkets: 1,
		MarketSlug:      "war-2026",
		MarketTitle:     "Will war happen 2026?",
		EventSlug:       "geo-2026",
		Direction:       "YES_BUY",
		Notional:        25000,
		Price:           0.15,
		Odds:            6.67,
		Payoff:          166750,
		ReasonHumanized: "new wallet · unusually large bet · high-impact market · near catalyst",
		Severity:        "HIGH",
	}, links)
	mustContain(t, out, "INSIDER")
	mustContain(t, out, "FIRST BIG BET")
	mustContain(t, out, "suspicious informed-flow candidate, not a legal insider claim")
	// must not claim legality
	low := strings.ToLower(out)
	for _, banned := range []string{"confirmed insider", "illegal insider", "knows outcome", "guaranteed"} {
		if strings.Contains(low, banned) {
			t.Fatalf("insider alert contains forbidden language %q:\n%s", banned, out)
		}
	}
}

func TestFormatClusterAlert(t *testing.T) {
	links := DefaultLinks()
	out := FormatClusterAlert(ClusterAlert{
		Direction:     "YES_BUY",
		MarketSlug:    "election-2026",
		MarketTitle:   "Will X win?",
		EventSlug:     "us-election",
		EventTitle:    "US Election 2026",
		TotalNotional: 65000,
		WalletCount:   4,
		WeightedPrice: 0.30,
		AverageOdds:   3.33,
		PayoffIfWin:   216667,
		WindowSeconds: 21600,
		Traders: []ClusterTrader{
			{WalletShort: "0xabc...", Pseudonym: "T1", ProfileSlug: "t1", Class: "shark", Score: 80, Notional: 20000},
			{WalletShort: "0xdef...", Pseudonym: "T2", ProfileSlug: "t2", Class: "insider_like", Score: 78, Notional: 15000},
		},
		ReasonCodes: []string{"MULTI_WALLET_ALIGNED", "NEAR_CATALYST"},
	}, links)
	mustContain(t, out, "CLUSTER BET DETECTED")
	mustContain(t, out, "YES_BUY")
	mustContain(t, out, "4 watched traders")
	mustContain(t, out, "6h")
	mustContain(t, out, "polymarket.com/event/us-election")
}

func TestFormatExitAlert_Shark(t *testing.T) {
	links := DefaultLinks()
	out := FormatExitAlert(ExitAlert{
		WalletShort: "0xab12cdef34", Pseudonym: "Trader", ProfileSlug: "trader",
		Class: "shark", MarketSlug: "ms", MarketTitle: "Q?", EventSlug: "es",
		Outcome: "Yes", OpenedDir: "YES_BUY",
		EntryPrice: 0.30, EntryNotional: 10000,
		ExitPrice: 0.45, ExitNotional: 15000,
		Status: "closed", PnLEstimate: 5000, PnLKnown: true,
		HeldDuration: "2h 15m",
	}, links)
	mustContain(t, out, "POSITION EXIT")
	mustContain(t, out, "Trader")
	mustContain(t, out, "Position: Yes")
	mustContain(t, out, "closed")
	mustContain(t, out, "+$5.0k")
	mustContain(t, out, "Held: 2h 15m")
}

func TestFormatExitAlert_Insider_LegalLanguage(t *testing.T) {
	links := DefaultLinks()
	out := FormatExitAlert(ExitAlert{
		WalletShort: "0xab12cdef34", Class: "insider_like",
		MarketSlug: "ms", MarketTitle: "Q?", EventSlug: "es",
		Outcome: "Yes", OpenedDir: "YES_BUY",
		EntryPrice: 0.15, EntryNotional: 30000,
		ExitPrice: 0.35, ExitNotional: 50000,
		Status: "partially_exited", PnLKnown: false,
	}, links)
	mustContain(t, out, "INSIDER-LIKE POSITION EXIT")
	mustContain(t, out, "lifecycle update for suspicious informed-flow candidate, not a legal insider claim")
	for _, banned := range []string{"confirmed insider", "illegal insider", "knows outcome", "guaranteed"} {
		if strings.Contains(strings.ToLower(out), banned) {
			t.Fatalf("forbidden language %q in exit alert", banned)
		}
	}
}

// TestMarkdownV2_NoUnescapedParens verifies that every ( in a rendered alert
// body is either preceded by \ (escaped literal) or by ] (Markdown link
// syntax). An unescaped ( causes a Telegram 400 parse error.
func TestMarkdownV2_NoUnescapedParens(t *testing.T) {
	links := DefaultLinks()

	cases := map[string]string{
		"SHARK_DISCOVERED": FormatSharkDiscovered(DiscoveryAlert{
			WalletShort:        "0xabc123def456",
			WalletFull:         "0xabc123def4560000000000000000000000abcdef",
			ProfileSlug:        "polyshark",
			Status:             "active",
			Score:              75,
			Confidence:         0.80,
			ProfitableCount:    7,
			LosingCount:        3,
			ClosedPositions:    10,
			ROI:                0.757,
			RealizedPnL:        45_000,
			HistoricalPnLKnown: true,
			EvaluatedFrom:      "2024-01-15",
			EvaluatedTo:        "2024-03-15",
			HistoryDays:        60,
			MarketSlug:         "election-2026",
			EventSlug:          "us-election",
		}, links),
		"SHARK_BET": FormatSharkBet(SharkBet{
			WalletShort: "0xabc123def456", WalletFull: "0xabc123def4560000000000000000000000abcdef",
			ProfileSlug: "polyshark", Class: "shark", Score: 82, Confidence: 0.78,
			ClosedPositionsCount: 42, HistoricalWinRate: 0.81, HistoricalROI: 0.45,
			AvgClosedStake: 18500, HistoricalPnLKnown: true, HistoricalPnL: 45000,
			MarketSlug: "election-2026", MarketTitle: "Will X win?", EventSlug: "us-election",
			Direction: "YES_BUY", Notional: 12000, Price: 0.42, Odds: 2.38, Payoff: 28560,
		}, links),
		"SHARK_BURST": FormatBurstAlert(BurstAlert{
			WalletShort: "0xabc123def456", WalletFull: "0xabc123def4560000000000000000000000abcdef",
			ProfileSlug: "polyshark", Class: "shark", BetsCount: 5, DistinctMarkets: 3,
			TotalNotional: 55000, WeightedPrice: 0.30, WindowMinutes: 15,
			Markets: []BurstMarketLine{
				{Slug: "m1", Title: "Q1?", Direction: "YES_BUY", Notional: 20000, BetCount: 3},
			},
		}, links),
		"INSIDER_DISCOVERED": FormatInsiderLikeDiscovered(DiscoveryAlert{
			WalletShort: "0xabc123def456", WalletFull: "0xabc123def4560000000000000000000000abcdef",
			ProfileSlug: "trader", Status: "active", Score: 70, Confidence: 0.70,
			ClosedWins: 3, ClosedLosses: 0, LatestBetUSD: 25000, LatestBetOdds: 5.0,
			MarketSlug: "m1", EventSlug: "e1",
		}, links),
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if err := checkNoUnescapedParens(body); err != nil {
				t.Fatalf("%s: unescaped paren found: %v\n---\n%s", name, err, body)
			}
		})
	}
}

// TestMarkdownV2_LinksRemainClickable verifies that link syntax [label](url)
// is produced correctly — label is escaped but URL is not double-escaped.
func TestMarkdownV2_LinksRemainClickable(t *testing.T) {
	links := DefaultLinks()
	out := FormatSharkBet(SharkBet{
		WalletShort: "0xabc123def456", WalletFull: "0xabc123def4560000000000000000000000abcdef",
		ProfileSlug: "polyshark", Class: "shark", Score: 75, Confidence: 0.75,
		MarketSlug: "election-2026", MarketTitle: "Q?", EventSlug: "us-election",
		Direction: "YES_BUY", Notional: 15000, Price: 0.3, Odds: 3.33, Payoff: 50000,
	}, links)
	// Link must contain the raw URL (not escaped)
	if !strings.Contains(out, "https://polymarket.com/profile/polyshark") {
		t.Fatalf("trader profile URL missing or double-escaped:\n%s", out)
	}
	if !strings.Contains(out, "https://polymarket.com/market/election-2026") {
		t.Fatalf("market URL missing or double-escaped:\n%s", out)
	}
	// Link labels must be escaped (no raw special chars in label area)
	if strings.Contains(out, "[Links: ") {
		t.Fatalf("Links: prefix must not be inside link label:\n%s", out)
	}
}

// checkNoUnescapedParens returns an error if s contains a ( that is neither
// preceded by \ (MarkdownV2 escape) nor preceded by ] (link syntax).
func checkNoUnescapedParens(s string) error {
	for i := 0; i < len(s); i++ {
		if s[i] != '(' {
			continue
		}
		if i > 0 && (s[i-1] == '\\' || s[i-1] == ']') {
			continue // escaped literal or link syntax
		}
		ctx := s
		if i > 20 {
			ctx = "..." + s[i-20:]
		}
		if len(ctx) > 60 {
			ctx = ctx[:60] + "..."
		}
		return fmt.Errorf("position %d: %q", i, ctx)
	}
	return nil
}

// stripEsc removes MarkdownV2 backslash escapes so tests can match against
// the human-readable substring without worrying about escaping rules.
func stripEsc(s string) string { return strings.ReplaceAll(s, `\`, "") }

func mustContain(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(stripEsc(s), sub) {
		t.Fatalf("expected output to contain %q, got:\n%s", sub, s)
	}
}
