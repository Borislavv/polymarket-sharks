package alerts

import (
	"strings"
	"testing"
)

func richSharkDiscovery() DiscoveryAlert {
	return DiscoveryAlert{
		WalletShort:          "0xf0fc…f309",
		WalletFull:           "0xf0fc2d0bf309f3aaaaaaaaaaaaaaaaaaaaabbbbb",
		Pseudonym:            "BigShark",
		Status:               "active",
		PromotionPath:        "realized_trading_shark",
		Score:                78,
		Confidence:           0.95,
		ScoringBasis:         "realized_trading_pnl",
		DataQuality:          "complete",
		ReasonHumanized:      "consistent realized trading profitability",
		ProfitableCount:      2020,
		LosingCount:          151,
		BreakevenCount:       0,
		RealizedCycles:       2171,
		ProfitableExitRate:   2020.0 / 2171.0,
		RealizedAvgROI:       0.1731,
		AvgTradeNotional:     8.5,
		MedianTradeNotional:  7.0,
		RealizedAvgNotional:  8.5,
		AvgWinUSD:            1.20,
		MedianWinUSD:         0.95,
		AvgLossUSD:           -0.85,
		MedianLossUSD:        -0.70,
		MaxWinUSD:            42.0,
		MaxLossUSD:           -12.5,
		GrossProfitUSD:       2424.0,
		GrossLossUSD:         -1494.0,
		RealizedTotalPnL:     930.0,
		RealizedProfitFactor: 1.62,
		HistoricalPnLKnown:   true,
		EvaluatedFrom:        "2025-08-12",
		EvaluatedTo:          "2026-05-25",
		HistoryDays:          286,
		TradesChecked:        3500,
		PositionsChecked:     1483,
		RealizedCyclesCheck:  2171,
		OpenUnresolvedCount:  214,
	}
}

func TestSharkDiscovered_PerformanceSection(t *testing.T) {
	body := FormatSharkDiscovered(richSharkDiscovery(), DefaultLinks())
	for _, want := range []string{
		"Performance:",
		"profitable exits: 2020/2171 (93%)",
		"realized PnL: $930",
		"profit factor: 1.62",
		"avg trade: $8.50",
		"median trade: $7.00",
		"avg win: $1.20",
		"avg loss: -$0.85",
		"max win/loss: $42.00 / -$12.50",
	} {
		if !strings.Contains(stripEsc(body), want) {
			t.Fatalf("missing %q in alert body:\n%s", want, body)
		}
	}
}

func TestSharkDiscovered_SampleSection(t *testing.T) {
	body := FormatSharkDiscovered(richSharkDiscovery(), DefaultLinks())
	for _, want := range []string{
		"Sample:",
		"evaluated period: 2025-08-12 → 2026-05-25 (286d)",
		"trades checked: 3500",
		"positions checked: 1483",
		"realized cycles checked: 2171",
		"open/unresolved excluded: 214",
		"scoring basis: realized trading pnl",
		"data quality: complete",
	} {
		if !strings.Contains(stripEsc(body), want) {
			t.Fatalf("missing %q in alert body:\n%s", want, body)
		}
	}
}

func TestSharkDiscovered_MissingMetricsRenderAsNA(t *testing.T) {
	body := FormatSharkDiscovered(DiscoveryAlert{
		WalletShort:   "0x970367…69c2",
		WalletFull:    "0x9703676286b93c2eca71ca96e8757104519a69c2",
		Status:        "active",
		PromotionPath: "historical_shark",
		Score:         55,
		Confidence:    0.5,
	}, DefaultLinks())
	for _, want := range []string{
		"profitable exits: n/a",
		"realized PnL: n/a",
		"profit factor: n/a",
		"avg trade: n/a",
		"avg win: n/a",
		"avg loss: n/a",
		"evaluated period: n/a",
		"trades checked: n/a",
		"positions checked: n/a",
		"data quality: n/a",
	} {
		if !strings.Contains(stripEsc(body), want) {
			t.Fatalf("missing %q in alert body:\n%s", want, body)
		}
	}
	// No fake "$0" for missing metrics.
	if strings.Contains(body, "avg trade: $0") || strings.Contains(body, "realized PnL: $0") {
		t.Fatalf("alert renders fake $0 for missing metrics:\n%s", body)
	}
}

func TestSharkDiscovered_OpenUnresolvedExcludedFromSuccessRate(t *testing.T) {
	// 100 profitable + 50 losing + 30 open. success_rate must be 100/150 = 67%,
	// NOT 100/180. The Performance line proves the denominator excludes open.
	a := DiscoveryAlert{
		WalletShort:         "0xa…b",
		WalletFull:          "0xa0000000000000000000000000000000000000bb",
		Status:              "active",
		ProfitableCount:     100,
		LosingCount:         50,
		BreakevenCount:      0,
		OpenUnresolvedCount: 30,
	}
	body := FormatSharkDiscovered(a, DefaultLinks())
	if !strings.Contains(stripEsc(body), "profitable exits: 100/150 (67%)") {
		t.Fatalf("expected success_rate denominator to exclude open positions; got body:\n%s", body)
	}
}

func TestSharkDiscovered_LinksClickable(t *testing.T) {
	body := FormatSharkDiscovered(richSharkDiscovery(), DefaultLinks())
	if !strings.Contains(body, "polymarket.com/profile/0xf0fc2d0bf309f3") {
		t.Fatalf("expected clickable trader URL with full wallet, got:\n%s", body)
	}
	// No raw "Links: Trader" without URL.
	for _, ln := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "Links:") {
			if !strings.Contains(ln, "](http") {
				t.Fatalf("Links footer without URL: %q", ln)
			}
		}
	}
}
