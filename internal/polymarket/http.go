// Package polymarket holds shared HTTP plumbing for the per-surface
// clients in subdirs: gamma, dataapi, clob, nextjs. Each surface owns its
// own rate limiter; the helper here applies retry/backoff on 429/5xx.
package polymarket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

// HTTPClient is the shared retrying client. One instance per surface.
type HTTPClient struct {
	Base       string
	HTTP       *http.Client
	Limiter    *rate.Limiter
	UserAgent  string
	MaxRetries int
}

// New constructs a client. rps == 0 disables limiting.
func New(base string, rps float64, timeout time.Duration) *HTTPClient {
	var lim *rate.Limiter
	if rps > 0 {
		lim = rate.NewLimiter(rate.Limit(rps), int(math.Max(1, math.Ceil(rps))))
	}
	return &HTTPClient{
		Base:       base,
		HTTP:       &http.Client{Timeout: timeout},
		Limiter:    lim,
		UserAgent:  "watchtower/1.0",
		MaxRetries: 3,
	}
}

// Do executes a GET request with rate limiting and bounded retries on
// 429 and 5xx. 4xx (other than 429) is non-retryable. Returns raw body.
func (c *HTTPClient) GET(ctx context.Context, url string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, url, nil)
}

func (c *HTTPClient) do(ctx context.Context, method, url string, body io.Reader) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if c.Limiter != nil {
			if err := c.Limiter.Wait(ctx); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, err
		}
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			if !shouldRetry(err) {
				return nil, err
			}
			if !sleepBackoff(ctx, attempt) {
				return nil, ctx.Err()
			}
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode/100 == 2 {
			return raw, nil
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode/100 == 5 {
			lastErr = fmt.Errorf("http %d: %s", resp.StatusCode, truncate(raw, 256))
			if !sleepBackoff(ctx, attempt) {
				return nil, ctx.Err()
			}
			continue
		}
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(raw, 256))
	}
	if lastErr == nil {
		lastErr = errors.New("retries exhausted")
	}
	return nil, lastErr
}

func sleepBackoff(ctx context.Context, attempt int) bool {
	// 100ms, 400ms, 1.6s, 6.4s, ...
	base := 100 * time.Millisecond
	d := time.Duration(1<<uint(2*attempt)) * base
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func shouldRetry(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
