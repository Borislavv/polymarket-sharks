package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGaugeOverwrites_NotAccumulates(t *testing.T) {
	SetGauge("test_gauge_a", 80)
	SetGauge("test_gauge_a", 80)
	if got := GaugeValue("test_gauge_a"); got != 80 {
		t.Fatalf("expected 80 after two equal sets, got %d", got)
	}
	SetGauge("test_gauge_a", 41)
	if got := GaugeValue("test_gauge_a"); got != 41 {
		t.Fatalf("expected 41 after overwrite, got %d", got)
	}
}

func TestCounterVsGauge(t *testing.T) {
	Add("test_counter_a", 5)
	Add("test_counter_a", 5)
	if got := *Default.Counter("test_counter_a"); got != 10 {
		t.Fatalf("expected counter=10, got %d", got)
	}
	SetGauge("test_gauge_b", 5)
	SetGauge("test_gauge_b", 5)
	if got := GaugeValue("test_gauge_b"); got != 5 {
		t.Fatalf("gauge must not sum: expected 5, got %d", got)
	}
}

func TestHandlerOutput(t *testing.T) {
	SetGauge("hotset_size", 80)
	Inc("ws_messages_total")
	srv := httptest.NewServer(Default.Handler())
	defer srv.Close()
	req := httptest.NewRecorder()
	Default.Handler().ServeHTTP(req, httptest.NewRequest("GET", "/metrics", nil))
	body := req.Body.String()
	if !strings.Contains(body, "wt_hotset_size 80") {
		t.Fatalf("hotset_size gauge missing in /metrics:\n%s", body)
	}
	if !strings.Contains(body, "wt_ws_messages_total") {
		t.Fatalf("ws_messages_total counter missing in /metrics:\n%s", body)
	}
}
