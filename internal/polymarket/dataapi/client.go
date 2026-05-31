// Package dataapi is the truth source for wallet identity, side, direction.
//
// Endpoints verified against the real https://data-api.polymarket.com on
// 2026-05-24. See README.md for the verification log and fixtures in
// /internal/polymarket/testdata/.
//
// All struct fields use FlexFloat/FlexInt for numeric values because the
// API mixes JSON numbers and numeric strings depending on path/version.
package dataapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
)

// Paths is the centralized endpoint configuration so an unstable API path
// can be overridden at runtime without recompiling.
type Paths struct {
	Holders   string // default: /holders
	Positions string // default: /positions
	Trades    string // default: /trades
	Activity  string // default: /activity
	Traded    string // default: /traded
	Value     string // default: /value
}

func DefaultPaths() Paths {
	return Paths{
		Holders:   "/holders",
		Positions: "/positions",
		Trades:    "/trades",
		Activity:  "/activity",
		Traded:    "/traded",
		Value:     "/value",
	}
}

type Client struct {
	HTTP  *polymarket.HTTPClient
	Paths Paths
}

func New(http *polymarket.HTTPClient) *Client {
	return &Client{HTTP: http, Paths: DefaultPaths()}
}

func NewWithPaths(http *polymarket.HTTPClient, p Paths) *Client {
	return &Client{HTTP: http, Paths: p}
}

// Holder is the inner holder record from the /holders endpoint.
// Real /holders response is [{token, holders:[Holder...]}, ...] — see HoldersGroup.
type Holder struct {
	ProxyWallet  string               `json:"proxyWallet"`
	Asset        string               `json:"asset"`
	OutcomeIndex int                  `json:"outcomeIndex"`
	Pseudonym    string               `json:"pseudonym"`
	Name         string               `json:"name"`
	Amount       polymarket.FlexFloat `json:"amount"`
}

// HoldersGroup is one outcome group in the /holders response.
type HoldersGroup struct {
	Token   string   `json:"token"`
	Holders []Holder `json:"holders"`
}

// FlatHolder is a denormalized record returned by GetHoldersByMarket for
// callers that don't care about the outcome grouping.
type FlatHolder struct {
	Token        string
	ProxyWallet  string
	Asset        string
	OutcomeIndex int
	Pseudonym    string
	Name         string
	Amount       float64
}

// GetHoldersByMarket fetches /holders?market=<conditionId> and flattens
// the per-token groups into a single slice. Raw payload is preserved.
func (c *Client) GetHoldersByMarket(ctx context.Context, conditionID string) ([]FlatHolder, []byte, error) {
	u := c.HTTP.Base + c.Paths.Holders + "?market=" + url.QueryEscape(conditionID)
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	var groups []HoldersGroup
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil, raw, fmt.Errorf("dataapi: parse holders: %w", err)
	}
	var out []FlatHolder
	for _, g := range groups {
		for _, h := range g.Holders {
			out = append(out, FlatHolder{
				Token:        g.Token,
				ProxyWallet:  h.ProxyWallet,
				Asset:        h.Asset,
				OutcomeIndex: h.OutcomeIndex,
				Pseudonym:    h.Pseudonym,
				Name:         h.Name,
				Amount:       h.Amount.Float64(),
			})
		}
	}
	return out, raw, nil
}

// Position is one position record from /positions.
// `realizedPnl` is per-position; sum across positions for total realized PnL.
type Position struct {
	ProxyWallet        string               `json:"proxyWallet"`
	ConditionID        string               `json:"conditionId"`
	Asset              string               `json:"asset"`
	OutcomeIndex       int                  `json:"outcomeIndex"`
	Outcome            string               `json:"outcome"`
	Size               polymarket.FlexFloat `json:"size"`
	AvgPrice           polymarket.FlexFloat `json:"avgPrice"`
	InitialValue       polymarket.FlexFloat `json:"initialValue"`
	CurrentValue       polymarket.FlexFloat `json:"currentValue"`
	CashPnl            polymarket.FlexFloat `json:"cashPnl"`
	PercentPnl         polymarket.FlexFloat `json:"percentPnl"`
	TotalBought        polymarket.FlexFloat `json:"totalBought"`
	RealizedPnl        polymarket.FlexFloat `json:"realizedPnl"`
	PercentRealizedPnl polymarket.FlexFloat `json:"percentRealizedPnl"`
	CurPrice           polymarket.FlexFloat `json:"curPrice"`
	Redeemable         bool                 `json:"redeemable"`
	Mergeable          bool                 `json:"mergeable"`
	Title              string               `json:"title"`
	Slug               string               `json:"slug"`
	EventID            string               `json:"eventId"`
	EventSlug          string               `json:"eventSlug"`
	OppositeOutcome    string               `json:"oppositeOutcome"`
	OppositeAsset      string               `json:"oppositeAsset"`
	EndDate            string               `json:"endDate"`
	NegativeRisk       bool                 `json:"negativeRisk"`
}

// GetUserPositions returns all positions for a wallet, used both as
// "current positions" (size>0) and as a source of realized PnL (sum of
// realizedPnl, including positions with size==0).
func (c *Client) GetUserPositions(ctx context.Context, user string) ([]Position, []byte, error) {
	u := c.HTTP.Base + c.Paths.Positions + "?user=" + url.QueryEscape(user)
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	var out []Position
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("dataapi: parse positions: %w", err)
	}
	return out, raw, nil
}

// ClosedPosition is a wallet's historical closed-position record. Source for
// v4 shark scoring (ROI / win-rate / avg-stake / realized PnL).
//
// Polymarket's data-api does not currently expose a dedicated /closed-positions
// endpoint. GetClosedPositionsPaginated below fetches /positions and projects
// closed rows through a stable boolean flag (IsClosed) so the scoring layer
// never has to re-infer "closed" from size==0 at score time. A position is
// considered closed iff size==0 (fully redeemed/sold) OR realized PnL has
// already been booked (any realizedPnl != 0). The latter captures partial
// closes; for ROI/win-rate we only count IsClosed=true rows with the realized
// portion of totalBought, so partial closes contribute partial data without
// double-counting open exposure.
type ClosedPosition struct {
	ConditionID        string
	OutcomeIndex       int
	Outcome            string
	EventID            string
	EventSlug          string
	Slug               string
	Title              string
	Size               float64
	AvgPrice           float64
	CurrentValue       float64
	TotalBought        float64
	RealizedPnL        float64
	CashPnL            float64
	PercentPnL         float64
	PercentRealizedPnL float64
	IsClosed           bool
	// ClosedAt is best-effort: Polymarket's /positions does not currently
	// emit a closed-at timestamp, so we leave this empty when unknown and
	// callers must treat it as nullable in storage.
	ClosedAt string
	// Raw is the original JSON for audit.
	Raw json.RawMessage
}

// PaginatedClosedPositions is the page result of GetClosedPositionsPaginated.
// HasMore is true iff the server returned a full page (caller may need to
// keep draining); the bool removes the ambiguity of "len<limit means done"
// when limit was honored exactly by chance.
type PaginatedClosedPositions struct {
	Items   []ClosedPosition
	HasMore bool
	Raw     []byte
}

// GetClosedPositionsPaginated fetches one page of the wallet's closed-position
// history. The Polymarket Data API does not expose `/closed-positions` as a
// distinct endpoint, so we hit `/positions?user=…` with limit&offset (the
// upstream accepts them and uses them when present; if it ignores them we'll
// see a short-then-empty pagination on the second call). The method then
// projects each row to a ClosedPosition where IsClosed=true iff the row
// represents a settled/exited slice of the wallet's history.
func (c *Client) GetClosedPositionsPaginated(ctx context.Context, user string, limit, offset int) (PaginatedClosedPositions, error) {
	q := url.Values{}
	q.Set("user", user)
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	if offset > 0 {
		q.Set("offset", fmt.Sprintf("%d", offset))
	}
	u := c.HTTP.Base + c.Paths.Positions + "?" + q.Encode()
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return PaginatedClosedPositions{Raw: raw}, err
	}
	var rows []Position
	if err := json.Unmarshal(raw, &rows); err != nil {
		return PaginatedClosedPositions{Raw: raw}, fmt.Errorf("dataapi: parse closed positions: %w", err)
	}
	out := PaginatedClosedPositions{Raw: raw, Items: make([]ClosedPosition, 0, len(rows))}
	for _, p := range rows {
		size := p.Size.Float64()
		realized := p.RealizedPnl.Float64()
		isClosed := size == 0 || realized != 0
		rawRow, _ := json.Marshal(p)
		out.Items = append(out.Items, ClosedPosition{
			ConditionID:        p.ConditionID,
			OutcomeIndex:       p.OutcomeIndex,
			Outcome:            p.Outcome,
			EventID:            p.EventID,
			EventSlug:          p.EventSlug,
			Slug:               p.Slug,
			Title:              p.Title,
			Size:               size,
			AvgPrice:           p.AvgPrice.Float64(),
			CurrentValue:       p.CurrentValue.Float64(),
			TotalBought:        p.TotalBought.Float64(),
			RealizedPnL:        realized,
			CashPnL:            p.CashPnl.Float64(),
			PercentPnL:         p.PercentPnl.Float64(),
			PercentRealizedPnL: p.PercentRealizedPnl.Float64(),
			IsClosed:           isClosed,
			Raw:                rawRow,
		})
	}
	out.HasMore = limit > 0 && len(rows) >= limit
	return out, nil
}

// MarketPositionsGroup mirrors the real /v1/market-positions response:
// `[ {token, positions:[...] }, ... ]` per outcome token.
type MarketPositionsGroup struct {
	Token     string     `json:"token"`
	Positions []Position `json:"positions"`
}

// MarketPositionSortBy enumerates verified sort modes for /v1/market-positions.
// Live API rejects other values; sort key validation lives here.
type MarketPositionSortBy string

const (
	SortByTokens      MarketPositionSortBy = "TOKENS"   // largest current holdings
	SortByCashPnl     MarketPositionSortBy = "CASH_PNL" // unrealized + realized aggregate
	SortByRealizedPnl MarketPositionSortBy = "REALIZED_PNL"
	SortByTotalPnl    MarketPositionSortBy = "TOTAL_PNL"
)

// GetMarketPositionsSorted calls /v1/market-positions with explicit sortBy.
// Returns flattened positions across all outcome tokens for the market.
// Real endpoint shape verified on 2026-05-24:
//
//	/v1/market-positions?market=<cid>&sortBy=TOKENS|CASH_PNL|REALIZED_PNL|TOTAL_PNL
func (c *Client) GetMarketPositionsSorted(ctx context.Context, conditionID string, sortBy MarketPositionSortBy, limit int) ([]Position, []byte, error) {
	q := url.Values{}
	q.Set("market", conditionID)
	if sortBy != "" {
		q.Set("sortBy", string(sortBy))
	}
	q.Set("sortDirection", "DESC")
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	u := c.HTTP.Base + "/v1/market-positions?" + q.Encode()
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	var groups []MarketPositionsGroup
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil, raw, fmt.Errorf("dataapi: parse market-positions: %w", err)
	}
	var out []Position
	for _, g := range groups {
		out = append(out, g.Positions...)
	}
	return out, raw, nil
}

// GetMarketPositions fetches positions filtered by market — used for the
// deep holder scan path. Same shape as /positions but with market filter.
func (c *Client) GetMarketPositions(ctx context.Context, conditionID string, limit int) ([]Position, []byte, error) {
	q := url.Values{}
	q.Set("market", conditionID)
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	u := c.HTTP.Base + c.Paths.Positions + "?" + q.Encode()
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	var out []Position
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("dataapi: parse market positions: %w", err)
	}
	return out, raw, nil
}

// Trade is the canonical record from /trades. Outcome strings are "Yes"/"No".
type Trade struct {
	TransactionHash string               `json:"transactionHash"`
	ProxyWallet     string               `json:"proxyWallet"`
	ConditionID     string               `json:"conditionId"`
	Asset           string               `json:"asset"`
	EventSlug       string               `json:"eventSlug"`
	Slug            string               `json:"slug"`
	Title           string               `json:"title"`
	Outcome         string               `json:"outcome"`
	OutcomeIndex    int                  `json:"outcomeIndex"`
	Side            string               `json:"side"`
	Price           polymarket.FlexFloat `json:"price"`
	Size            polymarket.FlexFloat `json:"size"`
	UsdcSize        polymarket.FlexFloat `json:"usdcSize"`
	Timestamp       polymarket.FlexInt   `json:"timestamp"`
	Pseudonym       string               `json:"pseudonym"`
	Name            string               `json:"name"`
}

// GetTradesPaginated fetches one page of /trades. The Polymarket Data API
// supports `limit` and `offset`; verified live. Caller drives pagination
// (callers in walletintel/runner.go drain history for shark qualification).
func (c *Client) GetTradesPaginated(ctx context.Context, user, market string, takerOnly bool, limit, offset int) ([]Trade, []byte, error) {
	q := url.Values{}
	if user != "" {
		q.Set("user", user)
	}
	if market != "" {
		q.Set("market", market)
	}
	q.Set("takerOnly", fmt.Sprintf("%t", takerOnly))
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	if offset > 0 {
		q.Set("offset", fmt.Sprintf("%d", offset))
	}
	u := c.HTTP.Base + c.Paths.Trades + "?" + q.Encode()
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	var out []Trade
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("dataapi: parse trades: %w", err)
	}
	return out, raw, nil
}

// GetTrades fetches /trades with optional user/market filters.
// Real API uses `takerOnly=true` by default; we pass it explicitly.
func (c *Client) GetTrades(ctx context.Context, user, market string, takerOnly bool, limit int) ([]Trade, []byte, error) {
	q := url.Values{}
	if user != "" {
		q.Set("user", user)
	}
	if market != "" {
		q.Set("market", market)
	}
	q.Set("takerOnly", fmt.Sprintf("%t", takerOnly))
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	u := c.HTTP.Base + c.Paths.Trades + "?" + q.Encode()
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	var out []Trade
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("dataapi: parse trades: %w", err)
	}
	return out, raw, nil
}

// Activity is the compact watched-wallet feed from /activity.
// `type` is e.g. "TRADE" or "REDEEM"; non-TRADE rows have empty side/outcome.
type Activity struct {
	TransactionHash string               `json:"transactionHash"`
	ProxyWallet     string               `json:"proxyWallet"`
	Type            string               `json:"type"`
	Asset           string               `json:"asset"`
	ConditionID     string               `json:"conditionId"`
	Side            string               `json:"side"`
	Outcome         string               `json:"outcome"`
	OutcomeIndex    int                  `json:"outcomeIndex"`
	Title           string               `json:"title"`
	Slug            string               `json:"slug"`
	EventSlug       string               `json:"eventSlug"`
	Price           polymarket.FlexFloat `json:"price"`
	Size            polymarket.FlexFloat `json:"size"`
	UsdcSize        polymarket.FlexFloat `json:"usdcSize"`
	Timestamp       polymarket.FlexInt   `json:"timestamp"`
	Name            string               `json:"name"`
	Pseudonym       string               `json:"pseudonym"`
}

func (c *Client) GetActivity(ctx context.Context, user string, limit int) ([]Activity, []byte, error) {
	q := url.Values{}
	q.Set("user", user)
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	u := c.HTTP.Base + c.Paths.Activity + "?" + q.Encode()
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	var out []Activity
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("dataapi: parse activity: %w", err)
	}
	return out, raw, nil
}

// UserSummary is the projection assembled from /traded + summed /positions.
type UserSummary struct {
	TotalTrades        int     // count of trades observed via /trades (best-effort)
	TotalMarketsTraded int     // from /traded?user=X
	RealizedPnL        float64 // sum of position.realizedPnl
	RealizedPnLKnown   bool
	ClosedPositions    int     // positions with size==0 (already redeemed/closed)
	CurrentValue       float64 // sum of position.currentValue

	// TotalCashPnL is sum of position.cashPnl across all positions returned by
	// /positions (no limit). cashPnl = realized + unrealized P&L per position,
	// which is what the Polymarket UI displays as "all-time P&L". This is the
	// closest public approximation to the wallet's global profitability.
	TotalCashPnL        float64
	TotalCashPnLKnown   bool
	TotalCashPnLSampleCount int // number of positions the sum is based on
}

type tradedResp struct {
	User   string `json:"user"`
	Traded int    `json:"traded"`
}

// GetTradedCount fetches /traded?user=X → {traded:N}.
func (c *Client) GetTradedCount(ctx context.Context, user string) (int, []byte, error) {
	u := c.HTTP.Base + c.Paths.Traded + "?user=" + url.QueryEscape(user)
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return 0, nil, err
	}
	var r tradedResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return 0, raw, fmt.Errorf("dataapi: parse traded: %w", err)
	}
	return r.Traded, raw, nil
}

type valueResp struct {
	User  string               `json:"user"`
	Value polymarket.FlexFloat `json:"value"`
}

// GetUserValue fetches /value?user=X → [{user, value}].
func (c *Client) GetUserValue(ctx context.Context, user string) (float64, []byte, error) {
	u := c.HTTP.Base + c.Paths.Value + "?user=" + url.QueryEscape(user)
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return 0, nil, err
	}
	var arr []valueResp
	if err := json.Unmarshal(raw, &arr); err != nil {
		return 0, raw, fmt.Errorf("dataapi: parse value: %w", err)
	}
	if len(arr) == 0 {
		return 0, raw, nil
	}
	return arr[0].Value.Float64(), raw, nil
}

// GetUserSummary aggregates /traded + /positions into a UserSummary.
// Missing endpoints degrade gracefully — caller inspects returned fields.
// TotalCashPnL is sum of cashPnl (realized + unrealized) — the same figure
// Polymarket's UI displays as "all-time P&L" on the public profile page.
func (c *Client) GetUserSummary(ctx context.Context, user string) (UserSummary, error) {
	s := UserSummary{}
	if traded, _, err := c.GetTradedCount(ctx, user); err == nil {
		s.TotalMarketsTraded = traded
	}
	if positions, _, err := c.GetUserPositions(ctx, user); err == nil {
		s.RealizedPnLKnown = len(positions) > 0
		s.TotalCashPnLKnown = len(positions) > 0
		s.TotalCashPnLSampleCount = len(positions)
		for _, p := range positions {
			s.RealizedPnL += p.RealizedPnl.Float64()
			s.CurrentValue += p.CurrentValue.Float64()
			s.TotalCashPnL += p.CashPnl.Float64()
			if p.Size.Float64() == 0 {
				s.ClosedPositions++
			}
		}
	}
	return s, nil
}
