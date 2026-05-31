package clob

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
)

func TestParseBook_RealFixture(t *testing.T) {
	raw, err := os.ReadFile("../testdata/clob_book.json")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(raw)
	}))
	defer srv.Close()
	c := NewREST(polymarket.New(srv.URL, 10, time.Second))
	b, _, err := c.GetBook(context.Background(), "0xT")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if b.Market == "" {
		t.Fatalf("missing market")
	}
	if len(b.Bids) == 0 {
		t.Fatalf("expected bids")
	}
	// BestBid must be > 0 and < 1
	bb := b.BestBid()
	if bb <= 0 || bb > 1 {
		t.Fatalf("bestbid out of range: %v", bb)
	}
}

func TestParseMidpoint_RealFixture(t *testing.T) {
	raw, err := os.ReadFile("../testdata/clob_midpoint.json")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(raw)
	}))
	defer srv.Close()
	c := NewREST(polymarket.New(srv.URL, 10, time.Second))
	mid, _, err := c.GetMidpoint(context.Background(), "0xT")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if mid <= 0 || mid > 1 {
		t.Fatalf("midpoint out of range: %v", mid)
	}
}
