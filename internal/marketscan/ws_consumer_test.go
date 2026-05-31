package marketscan

import (
	"encoding/json"
	"testing"
)

func TestParseWSPrices_BookSnapshot(t *testing.T) {
	raw := json.RawMessage(`{"event_type":"book","asset_id":"t","bids":[{"price":"0.30","size":"100"},{"price":"0.32","size":"50"}],"asks":[{"price":"0.36","size":"10"},{"price":"0.34","size":"20"}]}`)
	bb, ba, lt := parseWSPrices(raw)
	if bb != 0.32 {
		t.Fatalf("best bid %v want 0.32", bb)
	}
	if ba != 0.34 {
		t.Fatalf("best ask %v want 0.34", ba)
	}
	if lt != 0 {
		t.Fatalf("no lastTrade expected, got %v", lt)
	}
}

func TestParseWSPrices_PriceChangeBuy(t *testing.T) {
	raw := json.RawMessage(`{"event_type":"price_change","asset_id":"t","price":"0.45","side":"BUY"}`)
	bb, _, _ := parseWSPrices(raw)
	if bb != 0.45 {
		t.Fatalf("best bid %v want 0.45", bb)
	}
}

func TestParseWSPrices_LastTrade(t *testing.T) {
	raw := json.RawMessage(`{"event_type":"last_trade_price","asset_id":"t","price":"0.50","last_trade_price":"0.50"}`)
	_, _, lt := parseWSPrices(raw)
	if lt != 0.50 {
		t.Fatalf("last trade %v want 0.50", lt)
	}
}

func TestParseWSPrices_NoActionable(t *testing.T) {
	raw := json.RawMessage(`{"event_type":"tick_size_change","tick_size":"0.01"}`)
	bb, ba, lt := parseWSPrices(raw)
	if bb != 0 || ba != 0 || lt != 0 {
		t.Fatalf("expected no actionable prices, got %v %v %v", bb, ba, lt)
	}
}

func TestParseWSPrices_NumberOrString(t *testing.T) {
	raw := json.RawMessage(`{"event_type":"book","bids":[{"price":0.31,"size":1}],"asks":[{"price":"0.33","size":1}]}`)
	bb, ba, _ := parseWSPrices(raw)
	if bb != 0.31 || ba != 0.33 {
		t.Fatalf("mixed numeric forms not parsed: %v %v", bb, ba)
	}
}
