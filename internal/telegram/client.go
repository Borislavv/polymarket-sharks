// Package telegram is a tiny Bot-API HTTP client. Concentrated here so no
// worker ever calls Telegram directly — AlertRouter is the only caller.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/time/rate"
)

type Client struct {
	BotToken string
	HTTP     *http.Client
	Limiter  *rate.Limiter
}

func New(token string, rps float64, timeout time.Duration) *Client {
	if rps <= 0 {
		rps = 1
	}
	return &Client{
		BotToken: token,
		HTTP:     &http.Client{Timeout: timeout},
		Limiter:  rate.NewLimiter(rate.Limit(rps), 5),
	}
}

type sendMessageReq struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode,omitempty"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview,omitempty"`
}

type sendMessageResp struct {
	OK     bool `json:"ok"`
	Result struct {
		MessageID int64 `json:"message_id"`
	} `json:"result"`
	Description string `json:"description"`
}

// SendMessage posts a single Markdown(V2)-formatted message. Returns the
// Telegram message id on success.
func (c *Client) SendMessage(ctx context.Context, chatID, text string) (string, error) {
	if c.Limiter != nil {
		if err := c.Limiter.Wait(ctx); err != nil {
			return "", err
		}
	}
	body, _ := json.Marshal(sendMessageReq{
		ChatID:                chatID,
		Text:                  text,
		ParseMode:             "MarkdownV2",
		DisableWebPagePreview: true,
	})
	u := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", url.PathEscape(c.BotToken))
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("telegram %d: %s", resp.StatusCode, string(raw))
	}
	var r sendMessageResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	if !r.OK {
		return "", fmt.Errorf("telegram api error: %s", r.Description)
	}
	return fmt.Sprintf("%d", r.Result.MessageID), nil
}
