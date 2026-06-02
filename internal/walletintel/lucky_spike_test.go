package walletintel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
)

func TestScoreLuckySpike_PromotesOnSustainedHighFrequencyAndProfitPct(t *testing.T) {
	r := ScoreLuckySpike(LuckySpikeFacts{
		WeeklyTradeCount:        6_000, // > 1 trade / 2m over 7d
		WeeklyDistinctMarkets:   42,
		WeeklyRealizedCycles:    480,
		WeeklyProfitableCycles:  320,
		WeeklyLosingCycles:      160,
		WeeklyRealizedPnL:       40_000,
		WeeklyEntryNotional:     100_000,
		WeeklyProfitPct:         0.40,
		WeeklyProfitPctKnown:    true,
		WeeklyProfitSource:      "positions_cash_pnl",
		WeeklyProfitPositions:   90,
		MonthlyTradeCount:       24_000,
		MonthlyRealizedCycles:   1_200,
		MonthlyProfitPct:        0.35,
		MonthlyProfitPctKnown:   true,
		MonthlyProfitSource:     "positions_cash_pnl",
		MonthlyProfitPositions:  180,
		WeeklyCoverage:          7 * 24 * time.Hour,
		WeeklyAvgTradeInterval:  100 * time.Second,
		MonthlyAvgTradeInterval: 108 * time.Second,
		WeeklyAvgTradeNotional:  1_500,
		DataQuality:             "complete",
	}, LuckySpikeParams{})

	if !r.Promote {
		t.Fatalf("expected promote=true, got false: reasons=%v score=%d conf=%.2f", r.ReasonCodes, r.Score, r.Confidence)
	}
	if r.Strategy != "lucky_spike_score" {
		t.Fatalf("unexpected strategy: %s", r.Strategy)
	}
	if !contains(r.ReasonCodes, "WINDOW_PROFIT_PCT_ABOVE_30PCT") {
		t.Fatalf("expected WINDOW_PROFIT_PCT_ABOVE_30PCT in reasons: %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "SUSPECTED_LUCK_SPIKE_PATTERN") {
		t.Fatalf("expected SUSPECTED_LUCK_SPIKE_PATTERN in reasons: %v", r.ReasonCodes)
	}
}

func TestScoreLuckySpike_PromotesObservedCapAwareHighFrequencyAndProfitPct(t *testing.T) {
	r := ScoreLuckySpike(LuckySpikeFacts{
		WeeklyTradeCount:        3_500,
		WeeklyDistinctMarkets:   70,
		WeeklyRealizedCycles:    140,
		WeeklyProfitableCycles:  95,
		WeeklyLosingCycles:      45,
		WeeklyCoverage:          88 * time.Hour,
		WeeklyAvgTradeInterval:  90 * time.Second,
		WeeklyRealizedPnL:       6_100,
		WeeklyEntryNotional:     10_000,
		WeeklyProfitPct:         0.61,
		WeeklyProfitPctKnown:    true,
		WeeklyProfitSource:      "positions_cash_pnl",
		WeeklyProfitPositions:   70,
		MonthlyTradeCount:       3_500,
		MonthlyRealizedCycles:   140,
		MonthlyCoverage:         88 * time.Hour,
		MonthlyAvgTradeInterval: 90 * time.Second,
		MonthlyRealizedPnL:      6_100,
		MonthlyEntryNotional:    10_000,
		MonthlyProfitPct:        0.61,
		MonthlyProfitPctKnown:   true,
		MonthlyProfitSource:     "positions_cash_pnl",
		MonthlyProfitPositions:  70,
		DataQuality:             "partial_offset_cap",
		TradeHistoryPartialHint: "DATA_API_OFFSET_CAP_3000",
	}, LuckySpikeParams{})

	if !r.Promote {
		t.Fatalf("expected cap-aware observed sample to promote, got false: reasons=%v score=%d conf=%.2f", r.ReasonCodes, r.Score, r.Confidence)
	}
	if !contains(r.ReasonCodes, "WEEKLY_OBSERVED_HIGH_FREQUENCY") {
		t.Fatalf("expected observed high-frequency marker, got %v", r.ReasonCodes)
	}
	if !contains(r.ReasonCodes, "PARTIAL_HISTORY_LOWER_BOUND") {
		t.Fatalf("expected partial lower-bound marker, got %v", r.ReasonCodes)
	}
}

func TestScoreLuckySpike_DoesNotPromoteWhenFrequencyTooLow(t *testing.T) {
	r := ScoreLuckySpike(LuckySpikeFacts{
		WeeklyTradeCount:       1_000, // below 5040/week
		WeeklyRealizedCycles:   500,
		WeeklyProfitableCycles: 350,
		WeeklyLosingCycles:     150,
		WeeklyProfitPct:        0.45,
		WeeklyProfitPctKnown:   true,
		WeeklyProfitSource:     "positions_cash_pnl",
		WeeklyProfitPositions:  80,
		WeeklyCoverage:         7 * 24 * time.Hour,
		WeeklyAvgTradeInterval: 10 * time.Minute,
		DataQuality:            "complete",
	}, LuckySpikeParams{})

	if r.Promote {
		t.Fatalf("expected promote=false for low frequency, got true")
	}
	if !contains(r.ReasonCodes, "WEEKLY_FREQUENCY_TOO_LOW") {
		t.Fatalf("expected WEEKLY_FREQUENCY_TOO_LOW in reasons: %v", r.ReasonCodes)
	}
}

func TestScoreLuckySpike_PromotesHighFrequencyProfitEvenWithSmallPositionSample(t *testing.T) {
	r := ScoreLuckySpike(LuckySpikeFacts{
		WeeklyTradeCount:       16_947,
		WeeklyCoverage:         167*time.Hour + 55*time.Minute,
		WeeklyAvgTradeInterval: 36 * time.Second,
		WeeklyProfitPct:        0.52,
		WeeklyProfitPctKnown:   true,
		WeeklyProfitSource:     "profile_pnl_delta",
		WeeklyProfitPositions:  4,
		WeeklyRealizedPnL:      25.50,
		WeeklyEntryNotional:    49.02,
		DataQuality:            "complete",
	}, LuckySpikeParams{})

	if !r.Promote {
		t.Fatalf("expected high-frequency profitable wallet to promote despite small sample: reasons=%v score=%d conf=%.2f", r.ReasonCodes, r.Score, r.Confidence)
	}
	if !contains(r.ReasonCodes, "WEEKLY_REALIZED_SAMPLE_SMALL") {
		t.Fatalf("expected sample caveat reason, got %v", r.ReasonCodes)
	}
}

func TestScoreLuckySpike_DoesNotPromoteWhenProfitPctIsNotStrictlyAbove30Pct(t *testing.T) {
	r := ScoreLuckySpike(LuckySpikeFacts{
		WeeklyTradeCount:       6_000,
		WeeklyRealizedCycles:   300,
		WeeklyProfitableCycles: 180,
		WeeklyLosingCycles:     120,
		WeeklyProfitPct:        0.30, // strict > 0.30 required
		WeeklyProfitPctKnown:   true,
		WeeklyProfitSource:     "positions_cash_pnl",
		WeeklyProfitPositions:  80,
		WeeklyCoverage:         7 * 24 * time.Hour,
		WeeklyAvgTradeInterval: 110 * time.Second,
		DataQuality:            "complete",
	}, LuckySpikeParams{})

	if r.Promote {
		t.Fatalf("expected promote=false when profit_pct == 30%%")
	}
	if !contains(r.ReasonCodes, "WINDOW_PROFIT_PCT_TOO_LOW") {
		t.Fatalf("expected WINDOW_PROFIT_PCT_TOO_LOW in reasons: %v", r.ReasonCodes)
	}
}

func TestScoreLuckySpike_EmitsMissingDataWhenNoRealizedCycles(t *testing.T) {
	r := ScoreLuckySpike(LuckySpikeFacts{
		WeeklyTradeCount:       2_200,
		WeeklyRealizedCycles:   0,
		WeeklyCoverage:         7 * 24 * time.Hour,
		WeeklyAvgTradeInterval: 4 * time.Minute,
		DataQuality:            "complete",
	}, LuckySpikeParams{})

	if !contains(r.MissingData, "MISSING_POLYMARKET_POSITION_SAMPLE") {
		t.Fatalf("expected missing Polymarket position sample marker, got %v", r.MissingData)
	}
}

func TestLuckySpikeFetchWalletTrades_UsesActivityEndCursorAfterOffsetCap(t *testing.T) {
	var sawEndCursor bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/trades" {
			t.Fatalf("wallet history must use /activity, not /trades")
		}
		if r.URL.Path != "/activity" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("user") != "0xabc" || q.Get("type") != "TRADE" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		if q.Get("start") != "1000" || q.Get("sortBy") != "TIMESTAMP" || q.Get("sortDirection") != "DESC" {
			t.Fatalf("missing historical paging params: %s", r.URL.RawQuery)
		}

		offset, _ := strconv.Atoi(q.Get("offset"))
		end := q.Get("end")
		switch end {
		case "":
			rows := make([]map[string]any, 0, 500)
			for i := 0; i < 500; i++ {
				ts := 5000 - offset - i
				rows = append(rows, fakeActivityTrade("0xabc", ts, fmt.Sprintf("0x%04d_%03d", offset, i)))
			}
			_ = json.NewEncoder(w).Encode(rows)
		case "1500":
			sawEndCursor = true
			_ = json.NewEncoder(w).Encode([]map[string]any{
				fakeActivityTrade("0xabc", 1000, "0xolder"),
			})
		default:
			t.Fatalf("unexpected end cursor %q in query %s", end, r.URL.RawQuery)
		}
	}))
	defer srv.Close()

	worker := LuckySpikeWorker{
		DataAPI:                dataapi.New(polymarket.New(srv.URL, 0, time.Second)),
		WalletTradePageSize:    500,
		WalletActivityMaxPages: 10,
	}
	trades, partial, err := worker.fetchWalletTrades(context.Background(), "0xabc", time.Unix(1000, 0).UTC())
	if err != nil {
		t.Fatalf("fetchWalletTrades err: %v", err)
	}
	if partial != "" {
		t.Fatalf("expected complete history, got partial %q", partial)
	}
	if !sawEndCursor {
		t.Fatalf("expected crawler to continue with end=<oldest-1>")
	}
	if len(trades) != 3501 {
		t.Fatalf("expected 3501 deduped trades, got %d", len(trades))
	}
	if trades[0].Timestamp.Int64() != 1000 || trades[len(trades)-1].Timestamp.Int64() != 5000 {
		t.Fatalf("expected ascending sort from 1000 to 5000, got %d..%d", trades[0].Timestamp.Int64(), trades[len(trades)-1].Timestamp.Int64())
	}
}

func fakeActivityTrade(wallet string, ts int, tx string) map[string]any {
	return map[string]any{
		"type":            "TRADE",
		"proxyWallet":     wallet,
		"timestamp":       ts,
		"transactionHash": tx,
		"conditionId":     "0xc",
		"asset":           "0xa",
		"side":            "BUY",
		"outcome":         "Yes",
		"outcomeIndex":    0,
		"price":           0.42,
		"size":            10,
		"usdcSize":        4.2,
	}
}
