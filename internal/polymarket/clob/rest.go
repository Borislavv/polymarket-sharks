// Package clob is a minimal CLOB client. REST is used for orderbook snapshots
// and midpoint; WS subclient maintains hotset subscriptions.
//
// Verified against https://clob.polymarket.com on 2026-05-24:
//   - /book returns price/size as STRINGS, not numbers.
//   - /midpoint returns {"mid":"0.1745"} (string).
package clob

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
)

type RESTClient struct {
	HTTP *polymarket.HTTPClient
}

func NewREST(http *polymarket.HTTPClient) *RESTClient { return &RESTClient{HTTP: http} }

type BookLevel struct {
	Price polymarket.FlexFloat `json:"price"`
	Size  polymarket.FlexFloat `json:"size"`
}

type Book struct {
	Market    string      `json:"market"`
	AssetID   string      `json:"asset_id"`
	Timestamp string      `json:"timestamp"`
	Hash      string      `json:"hash"`
	Bids      []BookLevel `json:"bids"`
	Asks      []BookLevel `json:"asks"`
}

// BestBid returns the highest bid price; 0 if no bids. CLOB bids are
// ordered low→high in the response.
func (b *Book) BestBid() float64 {
	if b == nil || len(b.Bids) == 0 {
		return 0
	}
	max := 0.0
	for _, l := range b.Bids {
		v := l.Price.Float64()
		if v > max {
			max = v
		}
	}
	return max
}

// BestAsk returns the lowest ask price; 0 if no asks.
func (b *Book) BestAsk() float64 {
	if b == nil || len(b.Asks) == 0 {
		return 0
	}
	min := 1e18
	for _, l := range b.Asks {
		v := l.Price.Float64()
		if v > 0 && v < min {
			min = v
		}
	}
	if min == 1e18 {
		return 0
	}
	return min
}

func (c *RESTClient) GetBook(ctx context.Context, tokenID string) (*Book, []byte, error) {
	u := c.HTTP.Base + "/book?token_id=" + url.QueryEscape(tokenID)
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	var b Book
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, raw, fmt.Errorf("clob: parse book: %w", err)
	}
	return &b, raw, nil
}

type MidpointResp struct {
	Mid polymarket.FlexFloat `json:"mid"`
}

func (c *RESTClient) GetMidpoint(ctx context.Context, tokenID string) (float64, []byte, error) {
	u := c.HTTP.Base + "/midpoint?token_id=" + url.QueryEscape(tokenID)
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return 0, nil, err
	}
	var m MidpointResp
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, raw, fmt.Errorf("clob: parse midpoint: %w", err)
	}
	return m.Mid.Float64(), raw, nil
}
