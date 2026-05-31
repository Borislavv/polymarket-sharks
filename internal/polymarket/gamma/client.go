// Package gamma is a thin client for Polymarket's Gamma API.
// Verified against https://gamma-api.polymarket.com on 2026-05-24.
//
// Notes from the live API:
//   - market.clobTokenIds is a JSON-encoded STRING ("[\"id1\",\"id2\"]"),
//     not a raw array. Same for market.outcomes.
//   - market.volume / market.liquidity are NUMERIC STRINGS, not numbers.
//   - market.umaResolutionStatuses is a JSON-encoded string-array; umaResolutionStatus
//     is a top-level string ("proposed"/"resolved"/...).
//   - market.negRisk, event.negRisk are booleans.
//   - resolutionSource can be empty string.
package gamma

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
)

type Client struct {
	HTTP *polymarket.HTTPClient
}

func New(http *polymarket.HTTPClient) *Client { return &Client{HTTP: http} }

// Tag — event/market tag.
type Tag struct {
	ID    string `json:"id"`
	Slug  string `json:"slug"`
	Label string `json:"label"`
}

// Event mirrors the Gamma /events response. Unknown fields are ignored.
type Event struct {
	ID               string               `json:"id"`
	Slug             string               `json:"slug"`
	Title            string               `json:"title"`
	Ticker           string               `json:"ticker"`
	Description      string               `json:"description"`
	Category         string               `json:"category"`
	Tags             []Tag                `json:"tags"`
	Active           bool                 `json:"active"`
	Closed           bool                 `json:"closed"`
	Featured         bool                 `json:"featured"`
	Restricted       bool                 `json:"restricted"`
	NegRisk          bool                 `json:"negRisk"`
	UMAUncertainty   bool                 `json:"umaUncertainty"`
	ResolutionSource string               `json:"resolutionSource"`
	StartDate        string               `json:"startDate"`
	EndDate          string               `json:"endDate"`
	CreatedAt        string               `json:"createdAt"`
	UpdatedAt        string               `json:"updatedAt"`
	Volume           polymarket.FlexFloat `json:"volume"`
	Liquidity        polymarket.FlexFloat `json:"liquidity"`
	OpenInterest     polymarket.FlexFloat `json:"openInterest"`
	Markets          []Market             `json:"markets"`
}

// Market mirrors the inner market record.
type Market struct {
	ID                    string                     `json:"id"`
	ConditionID           string                     `json:"conditionId"`
	Slug                  string                     `json:"slug"`
	Question              string                     `json:"question"`
	Description           string                     `json:"description"`
	ResolutionSource      string                     `json:"resolutionSource"`
	Active                bool                       `json:"active"`
	Closed                bool                       `json:"closed"`
	NegRisk               bool                       `json:"negRisk"`
	NegRiskFeeBips        polymarket.FlexInt         `json:"negRiskFeeBips"`
	UMABond               polymarket.FlexFloat       `json:"umaBond"`
	UMAReward             polymarket.FlexFloat       `json:"umaReward"`
	UMAResolutionStatus   string                     `json:"umaResolutionStatus"`
	UMAResolutionStatuses polymarket.FlexStringSlice `json:"umaResolutionStatuses"`
	Volume                polymarket.FlexFloat       `json:"volume"`
	Liquidity             polymarket.FlexFloat       `json:"liquidity"`
	ClobTokenIDs          polymarket.FlexStringSlice `json:"clobTokenIds"`
	Outcomes              polymarket.FlexStringSlice `json:"outcomes"`
	StartDate             string                     `json:"startDate"`
	EndDate               string                     `json:"endDate"`
	Image                 string                     `json:"image"`
	Icon                  string                     `json:"icon"`
}

// ListEventsParams supported filters.
type ListEventsParams struct {
	Tag       string // tag_slug
	Active    *bool
	Closed    *bool
	Limit     int
	Offset    int
	Order     string // e.g. "volume"
	Ascending *bool
}

// ListEvents calls /events. Returns parsed events and the raw payload.
func (c *Client) ListEvents(ctx context.Context, p ListEventsParams) ([]Event, []byte, error) {
	u := c.HTTP.Base + "/events"
	q := url.Values{}
	if p.Tag != "" {
		q.Set("tag_slug", p.Tag)
	}
	if p.Active != nil {
		q.Set("active", fmt.Sprintf("%t", *p.Active))
	}
	if p.Closed != nil {
		q.Set("closed", fmt.Sprintf("%t", *p.Closed))
	}
	if p.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", p.Limit))
	}
	if p.Offset > 0 {
		q.Set("offset", fmt.Sprintf("%d", p.Offset))
	}
	if p.Order != "" {
		q.Set("order", p.Order)
	}
	if p.Ascending != nil {
		q.Set("ascending", fmt.Sprintf("%t", *p.Ascending))
	}
	if e := q.Encode(); e != "" {
		u = u + "?" + e
	}
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	var out []Event
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("gamma: parse events: %w", err)
	}
	return out, raw, nil
}
