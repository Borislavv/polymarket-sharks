package alerts

import (
	"strings"
	"testing"
)

const fullWallet = "0x9703676286b93c2eca71ca96e8757104519a69c2"

func TestTraderURL_RejectsTruncatedDisplayString(t *testing.T) {
	// "0x970367…69c2" is the WalletShort display variant — it must NOT be
	// silently converted into a URL (the ellipsis would make Telegram
	// render an unclickable link).
	l := DefaultLinks()
	if got := l.Trader("", "0x970367…69c2"); got != "" {
		t.Fatalf("expected empty URL for truncated display string, got %q", got)
	}
}

func TestTraderURL_AcceptsFullWalletAddress(t *testing.T) {
	l := DefaultLinks()
	got := l.Trader("", fullWallet)
	if got != "https://polymarket.com/profile/"+fullWallet {
		t.Fatalf("unexpected URL: %s", got)
	}
}

func TestTraderURL_PrefersProfileSlugOverWallet(t *testing.T) {
	l := DefaultLinks()
	got := l.Trader("polyshark", fullWallet)
	if got != "https://polymarket.com/profile/polyshark" {
		t.Fatalf("expected profile slug used, got %s", got)
	}
}

func TestTraderLink_MarkdownFormat(t *testing.T) {
	l := DefaultLinks()
	got := l.TraderLink("PolyShark", "polyshark", fullWallet)
	if got == "" {
		t.Fatalf("expected clickable markdown link")
	}
	if !strings.HasPrefix(got, "[") || !strings.Contains(got, "](") {
		t.Fatalf("not a markdown link: %s", got)
	}
	if !strings.Contains(got, "polymarket.com/profile/polyshark") {
		t.Fatalf("URL missing: %s", got)
	}
}

func TestTraderLink_EmptyWhenNoIdentifier(t *testing.T) {
	l := DefaultLinks()
	if got := l.TraderLink("Trader", "", ""); got != "" {
		t.Fatalf("expected empty when nothing usable, got %q", got)
	}
	if got := l.TraderLink("Trader", "", "0x970367…69c2"); got != "" {
		t.Fatalf("expected empty for truncated wallet, got %q", got)
	}
}

func TestMarketLink_SkipsWhenSlugMissing(t *testing.T) {
	l := DefaultLinks()
	if got := l.MarketLink("Market", ""); got != "" {
		t.Fatalf("expected empty when slug missing, got %q", got)
	}
}

func TestEventLink_SkipsWhenSlugMissing(t *testing.T) {
	l := DefaultLinks()
	if got := l.EventLink("Event", ""); got != "" {
		t.Fatalf("expected empty when slug missing, got %q", got)
	}
}

func TestJoinLinks_DropsEmptyPieces(t *testing.T) {
	got := JoinLinks("", "[A](https://a)", "", "[B](https://b)")
	if got != "[A](https://a) · [B](https://b)" {
		t.Fatalf("unexpected join: %q", got)
	}
	if JoinLinks("", "", "") != "" {
		t.Fatalf("expected empty when all pieces are empty")
	}
}

func TestFormatSharkDiscovered_ContainsClickableTraderLink(t *testing.T) {
	l := DefaultLinks()
	out := FormatSharkDiscovered(DiscoveryAlert{
		WalletShort:        "0x970367…69c2",
		WalletFull:         fullWallet,
		Status:             "active",
		PromotionPath:      "historical_shark",
		Score:              82,
		Confidence:         0.85,
		ClosedPositions:    784,
		WinRate:            0.7576,
		ROI:                0.1077,
		AvgClosedStake:     41377,
		RealizedPnL:        3494481,
		HistoricalPnLKnown: true,
		ReasonHumanized:    "passes v4 historical-shark gates",
	}, l)
	if !strings.Contains(out, "Links: ") {
		t.Fatalf("missing Links footer: %s", out)
	}
	want := "https://polymarket.com/profile/" + fullWallet
	if !strings.Contains(out, want) {
		t.Fatalf("missing clickable trader URL %q, got: %s", want, out)
	}
	// No "Links: Trader\n" without URL.
	if strings.Contains(out, "Links: Trader\n") || strings.HasSuffix(strings.TrimSpace(out), "Links: Trader") {
		t.Fatalf("Links footer rendered plain 'Trader' label without URL: %s", out)
	}
}

func TestFormatInsiderLikeDiscovered_ContainsClickableWalletLink(t *testing.T) {
	l := DefaultLinks()
	out := FormatInsiderLikeDiscovered(DiscoveryAlert{
		WalletShort:    "0xabc…def",
		WalletFull:     "0xabcdef0123456789abcdef0123456789abcdef01",
		Status:         "active",
		Score:          70,
		Confidence:     0.8,
		LifetimeTrades: 1,
		StreakState:    "clean",
	}, l)
	if !strings.Contains(out, "polymarket.com/profile/0xabcdef") {
		t.Fatalf("expected full-wallet URL, got: %s", out)
	}
}

func TestFormatSharkBet_LinksOmittedWhenSlugsMissing(t *testing.T) {
	l := DefaultLinks()
	// Market/Event slugs absent → those links must be dropped, Trader stays.
	out := FormatSharkBet(SharkBet{
		WalletShort:          "0x970367…69c2",
		WalletFull:           fullWallet,
		Pseudonym:            "PolyShark",
		ProfileSlug:          "polyshark",
		Class:                "shark",
		Score:                82,
		Confidence:           0.78,
		ClosedPositionsCount: 42,
		HistoricalROI:        0.45,
		AvgClosedStake:       18500,
		HistoricalPnL:        45000,
		HistoricalPnLKnown:   true,
		Direction:            "YES_BUY",
		Notional:             12000,
		Price:                0.42,
		Odds:                 2.38,
	}, l)
	if !strings.Contains(out, "polymarket.com/profile/polyshark") {
		t.Fatalf("expected trader link, got: %s", out)
	}
	// Should not invent a fake Market/Event link
	if strings.Contains(out, "polymarket.com/market/") {
		t.Fatalf("unexpected market URL when slug missing: %s", out)
	}
	if strings.Contains(out, "polymarket.com/event/") {
		t.Fatalf("unexpected event URL when slug missing: %s", out)
	}
}

func TestFormatSharkBet_AllLinksWhenAllDataPresent(t *testing.T) {
	l := DefaultLinks()
	out := FormatSharkBet(SharkBet{
		WalletShort: "0x970367…69c2",
		WalletFull:  fullWallet,
		ProfileSlug: "polyshark",
		Class:       "shark",
		Score:       82,
		Confidence:  0.78,
		MarketSlug:  "us-election-2026",
		EventSlug:   "us-election",
		Direction:   "YES_BUY",
		Notional:    12000,
		Price:       0.42,
		Odds:        2.38,
	}, l)
	for _, want := range []string{
		"polymarket.com/market/us-election-2026",
		"polymarket.com/event/us-election",
		"polymarket.com/profile/polyshark",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in alert, got: %s", want, out)
		}
	}
}

func TestNoAlertContainsBarePlainTraderLabel(t *testing.T) {
	// Sweep all the discovery / bet formatters with a typical truncated
	// WalletShort but no WalletFull. The Links footer should be EMPTY,
	// never "Links: Trader" without a URL.
	l := DefaultLinks()
	cases := map[string]string{
		"shark_discovered": FormatSharkDiscovered(DiscoveryAlert{
			WalletShort: "0x970367…69c2", Status: "active",
		}, l),
		"insider_discovered": FormatInsiderLikeDiscovered(DiscoveryAlert{
			WalletShort: "0x970367…69c2", Status: "active",
		}, l),
		"shark_bet": FormatSharkBet(SharkBet{
			WalletShort: "0x970367…69c2", Class: "shark", Direction: "YES_BUY",
		}, l),
		"insider_bet": FormatInsiderBet(InsiderBet{
			WalletShort: "0x970367…69c2", Severity: "HIGH", Direction: "YES_BUY",
		}, l),
		"burst": FormatBurstAlert(BurstAlert{
			WalletShort: "0x970367…69c2",
		}, l),
		"demoted": FormatWalletDemoted(DiscoveryAlert{
			WalletShort: "0x970367…69c2", Class: "shark", Status: "unactual",
		}, l),
	}
	for name, body := range cases {
		// any line that says "Links: " must contain at least one "](http"
		lines := strings.Split(body, "\n")
		for _, ln := range lines {
			if !strings.HasPrefix(strings.TrimSpace(ln), "Links:") {
				continue
			}
			if !strings.Contains(ln, "](http") {
				t.Fatalf("%s: Links footer without URL: %q\n--full--\n%s", name, ln, body)
			}
		}
	}
}
