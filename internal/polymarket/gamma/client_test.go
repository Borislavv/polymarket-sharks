package gamma

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
)

func TestParseRealFixture(t *testing.T) {
	raw, err := os.ReadFile("../testdata/gamma_events.json")
	if err != nil {
		t.Fatalf("missing fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(raw)
	}))
	defer srv.Close()

	c := New(polymarket.New(srv.URL, 10, time.Second))
	events, _, err := c.ListEvents(context.Background(), ListEventsParams{Limit: 2})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected events")
	}
	// real markets must have clobTokenIds parsed from JSON-encoded string
	found := false
	for _, e := range events {
		for _, m := range e.Markets {
			if len(m.ClobTokenIDs) > 0 {
				found = true
				if len(m.Outcomes) == 0 {
					t.Fatalf("expected outcomes parsed alongside clobTokenIds")
				}
			}
		}
	}
	if !found {
		t.Fatalf("no markets with clobTokenIds parsed")
	}
}

func TestParseHandlesNumericStrings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{
		  "id":"1","slug":"e","title":"E","active":true,"closed":false,
		  "volume":"123.45","liquidity":null,
		  "markets":[{"conditionId":"0xC","slug":"m","question":"Q?",
		    "active":true,"closed":false,"negRisk":false,
		    "clobTokenIds":"[\"t1\",\"t2\"]","outcomes":"[\"Yes\",\"No\"]",
		    "volume":"678","liquidity":"42.0",
		    "umaResolutionStatus":"proposed",
		    "umaResolutionStatuses":"[\"proposed\"]"}]}]`))
	}))
	defer srv.Close()
	c := New(polymarket.New(srv.URL, 10, time.Second))
	events, _, err := c.ListEvents(context.Background(), ListEventsParams{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(events) != 1 || events[0].Volume.Float64() != 123.45 {
		t.Fatalf("expected volume 123.45, got %+v", events[0].Volume.Float64())
	}
	m := events[0].Markets[0]
	if len(m.ClobTokenIDs) != 2 || m.ClobTokenIDs[0] != "t1" {
		t.Fatalf("clob token ids parse failed: %v", m.ClobTokenIDs)
	}
	if m.Outcomes[0] != "Yes" || m.Outcomes[1] != "No" {
		t.Fatalf("outcomes parse failed: %v", m.Outcomes)
	}
	if m.UMAResolutionStatus != "proposed" {
		t.Fatalf("uma status: %v", m.UMAResolutionStatus)
	}
}
