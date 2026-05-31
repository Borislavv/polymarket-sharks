// Package nextjs is the OPTIONAL news adapter that scrapes Polymarket's
// internal Next.js event JSON. Internal/non-official source: failures
// must NOT crash core surveillance. Feature-flagged via NEXTJS_NEWS_ENABLED.
//
// Verified shape (2026-05-24): /event/<slug> HTML embeds {"buildId":"..."}
// inside an <script id="__NEXT_DATA__"> blob. The data endpoint
// /_next/data/<buildId>/event/<slug>.json returns pageProps.dehydratedState
// (React Query SSR). News-like items live under:
//   - queryKey starting with "annotations" (event annotations)
//   - queryKey starting with "/api/event/slug" — event metadata
//   - news / timeline / chartAnnotations on the event dict, if present
package nextjs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Client struct {
	Base string
	HTTP *http.Client
	TTL  time.Duration

	mu        sync.Mutex
	buildID   string
	buildIDAt time.Time
}

func New(base string, timeout, ttl time.Duration) *Client {
	if base == "" {
		base = "https://polymarket.com"
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &Client{
		Base: base,
		HTTP: &http.Client{
			Timeout: timeout,
			// follow 307 redirects (e.g. /en/event ↔ /event)
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
		TTL: ttl,
	}
}

var buildIDRegex = regexp.MustCompile(`"buildId":"([^"]+)"`)

func (c *Client) ResolveBuildID(ctx context.Context, slug string) (string, error) {
	c.mu.Lock()
	if c.buildID != "" && time.Since(c.buildIDAt) < c.TTL {
		id := c.buildID
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	url := c.Base + "/event/" + slug
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "watchtower/1.0")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("nextjs: html status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	m := buildIDRegex.FindStringSubmatch(string(raw))
	if len(m) < 2 {
		return "", errors.New("nextjs: buildId not found in HTML")
	}
	id := m[1]
	c.mu.Lock()
	c.buildID = id
	c.buildIDAt = time.Now()
	c.mu.Unlock()
	return id, nil
}

// NewsItem is the normalized internal record we route to NEWS channel.
type NewsItem struct {
	Title     string
	Summary   string
	URL       string
	Source    string // "annotations" | "event.news" | "timeline" | "chart-annotation"
	Timestamp time.Time
}

// EventPayload is the parsed dehydratedState bundle.
type EventPayload struct {
	Slug  string
	Title string
	News  []NewsItem
	Raw   []byte
}

// FetchEvent retrieves /_next/data/<buildId>/event/<slug>.json, walks the
// React Query SSR cache, and extracts every news-like item.
func (c *Client) FetchEvent(ctx context.Context, slug string) (*EventPayload, error) {
	for attempt := 0; attempt < 2; attempt++ {
		id, err := c.ResolveBuildID(ctx, slug)
		if err != nil {
			return nil, err
		}
		url := fmt.Sprintf("%s/_next/data/%s/event/%s.json", c.Base, id, slug)
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "watchtower/1.0")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && attempt == 0 {
			c.mu.Lock()
			c.buildID = ""
			c.mu.Unlock()
			continue
		}
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("nextjs: json status %d", resp.StatusCode)
		}
		p, err := parseEventPayload(raw, slug)
		if err != nil {
			return nil, err
		}
		return p, nil
	}
	return nil, errors.New("nextjs: failed after buildId refresh")
}

// parseEventPayload is exported via FetchEvent. Splitting it makes testing
// trivial: feed any raw JSON and verify extraction.
func parseEventPayload(raw []byte, slug string) (*EventPayload, error) {
	var envelope struct {
		PageProps struct {
			DehydratedState struct {
				Queries []rqQuery `json:"queries"`
			} `json:"dehydratedState"`
		} `json:"pageProps"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("nextjs: parse envelope: %w", err)
	}
	out := &EventPayload{Slug: slug, Raw: raw}
	for _, q := range envelope.PageProps.DehydratedState.Queries {
		kind := qkPrefix(q.QueryKey)
		if kind == "" {
			continue
		}
		switch {
		case kind == "annotations":
			items, _ := extractAnnotations(q.State.Data)
			for _, it := range items {
				it.Source = "annotations"
				out.News = append(out.News, it)
			}
		case kind == "/api/event/slug":
			title, news := extractEventDetail(q.State.Data)
			if title != "" {
				out.Title = title
			}
			out.News = append(out.News, news...)
		case strings.HasPrefix(kind, "chart-annotations"):
			items, _ := extractAnnotations(q.State.Data)
			for _, it := range items {
				it.Source = "chart-annotation"
				out.News = append(out.News, it)
			}
		}
	}
	return out, nil
}

type rqQuery struct {
	QueryKey []any `json:"queryKey"`
	State    struct {
		Data json.RawMessage `json:"data"`
	} `json:"state"`
}

func qkPrefix(qk []any) string {
	if len(qk) == 0 {
		return ""
	}
	if s, ok := qk[0].(string); ok {
		return s
	}
	return ""
}

// extractAnnotations handles either an array of annotation objects or
// a single object with an `annotations` array.
func extractAnnotations(raw json.RawMessage) ([]NewsItem, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var arr []annotationDTO
	if err := json.Unmarshal(raw, &arr); err == nil {
		return annotationsToNews(arr), nil
	}
	var wrapper struct {
		Annotations []annotationDTO `json:"annotations"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil {
		return annotationsToNews(wrapper.Annotations), nil
	}
	return nil, nil
}

type annotationDTO struct {
	ID        any    `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Summary   string `json:"summary"`
	URL       string `json:"url"`
	Source    string `json:"source"`
	Timestamp any    `json:"timestamp"`
	Date      string `json:"date"`
	CreatedAt string `json:"createdAt"`
}

func annotationsToNews(in []annotationDTO) []NewsItem {
	var out []NewsItem
	for _, a := range in {
		if a.Title == "" && a.Body == "" {
			continue
		}
		ts := parseAnyTime(a.Timestamp, a.Date, a.CreatedAt)
		title := strings.TrimSpace(a.Title)
		if title == "" {
			title = truncate(strings.TrimSpace(a.Body), 120)
		}
		summary := strings.TrimSpace(a.Summary)
		if summary == "" {
			summary = strings.TrimSpace(a.Body)
		}
		out = append(out, NewsItem{
			Title:     title,
			Summary:   summary,
			URL:       a.URL,
			Timestamp: ts,
		})
	}
	return out
}

// extractEventDetail pulls title plus any embedded news/timeline arrays
// from the /api/event/slug query payload.
func extractEventDetail(raw json.RawMessage) (string, []NewsItem) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var ev struct {
		Slug             string          `json:"slug"`
		Title            string          `json:"title"`
		News             []annotationDTO `json:"news"`
		Timeline         []annotationDTO `json:"timeline"`
		ChartAnnotations []annotationDTO `json:"chartAnnotations"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return "", nil
	}
	var out []NewsItem
	for _, it := range annotationsToNews(ev.News) {
		it.Source = "event.news"
		out = append(out, it)
	}
	for _, it := range annotationsToNews(ev.Timeline) {
		it.Source = "timeline"
		out = append(out, it)
	}
	for _, it := range annotationsToNews(ev.ChartAnnotations) {
		it.Source = "chart-annotation"
		out = append(out, it)
	}
	return ev.Title, out
}

func parseAnyTime(v ...any) time.Time {
	for _, x := range v {
		switch t := x.(type) {
		case string:
			if t == "" {
				continue
			}
			for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02"} {
				if ts, err := time.Parse(layout, t); err == nil {
					return ts
				}
			}
		case float64:
			// epoch (seconds or millis)
			if t > 1e12 {
				return time.UnixMilli(int64(t))
			}
			return time.Unix(int64(t), 0)
		case int64:
			return time.Unix(t, 0)
		}
	}
	return time.Time{}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
