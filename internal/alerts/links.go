package alerts

import (
	"fmt"
	"strings"

	"github.com/Borislavv/polymarket-sharks/internal/telegram"
)

// LinkBuilder mints user-facing links to Polymarket surfaces. We never
// invent URLs — only mint the ones rooted in known slug fields.
type LinkBuilder struct {
	BaseEvent     string // https://polymarket.com/event
	BaseMarket    string // https://polymarket.com/market
	BaseProfile   string // https://polymarket.com/profile
	BaseTx        string // optional, e.g. https://polygonscan.com/tx
	BaseDashboard string // optional internal
}

func DefaultLinks() LinkBuilder {
	return LinkBuilder{
		BaseEvent:   "https://polymarket.com/event",
		BaseMarket:  "https://polymarket.com/market",
		BaseProfile: "https://polymarket.com/profile",
	}
}

// Event returns the canonical event URL, or "" if no slug.
func (l LinkBuilder) Event(slug string) string {
	if slug == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s", l.BaseEvent, slug)
}

// Market returns the canonical market URL, or "" if no slug.
func (l LinkBuilder) Market(slug string) string {
	if slug == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s", l.BaseMarket, slug)
}

// Trader returns the canonical trader URL. We prefer the profile slug when
// available (it's the username-style URL Polymarket displays), and fall back
// to the 0x… wallet address only when the value looks like a real wallet —
// i.e. starts with 0x, is the expected length, and contains no ellipsis or
// other formatting characters. Returns "" if no usable identifier exists.
func (l LinkBuilder) Trader(profileSlug, wallet string) string {
	if s := strings.TrimSpace(profileSlug); s != "" {
		return fmt.Sprintf("%s/%s", l.BaseProfile, s)
	}
	if !looksLikeWalletAddress(wallet) {
		return ""
	}
	return fmt.Sprintf("%s/%s", l.BaseProfile, strings.ToLower(strings.TrimSpace(wallet)))
}

// Tx returns the canonical chain explorer URL for a tx, or "" if no base or hash.
func (l LinkBuilder) Tx(txHash string) string {
	if l.BaseTx == "" || strings.TrimSpace(txHash) == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s", l.BaseTx, strings.ToLower(strings.TrimSpace(txHash)))
}

// Dashboard returns the optional internal dashboard URL when configured.
func (l LinkBuilder) Dashboard() string { return l.BaseDashboard }

// --- Markdown rendering helpers ---
//
// These centralize the parse-mode-aware rendering so call sites stop using
// fmt.Sprintf with MarkdownV2 link syntax inline. All four return either a
// clickable [label](url) or an empty string — they NEVER return a bare label
// dressed up as a link, because that's exactly the bug that produced
// "Links: Trader" without a URL.

// TraderLink returns a clickable Markdown link to the trader's profile.
// Pseudonym/wallet-short can be used as visible label; pass the FULL wallet
// (or profile slug) for the URL. Empty when neither identifier is usable.
func (l LinkBuilder) TraderLink(label, profileSlug, wallet string) string {
	u := l.Trader(profileSlug, wallet)
	if u == "" {
		return ""
	}
	return mdLinkEscaped(label, u)
}

// MarketLink returns a clickable Markdown link to a Polymarket market.
func (l LinkBuilder) MarketLink(label, slug string) string {
	u := l.Market(slug)
	if u == "" {
		return ""
	}
	return mdLinkEscaped(label, u)
}

// EventLink returns a clickable Markdown link to a Polymarket event.
func (l LinkBuilder) EventLink(label, slug string) string {
	u := l.Event(slug)
	if u == "" {
		return ""
	}
	return mdLinkEscaped(label, u)
}

// TxLink returns a clickable Markdown link to a chain explorer for the tx.
// Empty when no BaseTx is configured — we never invent explorer URLs.
func (l LinkBuilder) TxLink(label, txHash string) string {
	u := l.Tx(txHash)
	if u == "" {
		return ""
	}
	return mdLinkEscaped(label, u)
}

// DashboardLink returns a clickable Markdown link to the internal dashboard
// when configured. Empty otherwise.
func (l LinkBuilder) DashboardLink(label string) string {
	u := l.Dashboard()
	if u == "" {
		return ""
	}
	return mdLinkEscaped(label, u)
}

// JoinLinks renders a single "Links: …" line from any non-empty pieces.
// Pieces are typically the *Link helpers above. If everything is empty, the
// helper returns "" so callers can skip emitting the line entirely.
func JoinLinks(pieces ...string) string {
	out := make([]string, 0, len(pieces))
	for _, p := range pieces {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " · ")
}

// looksLikeWalletAddress rejects truncated/display variants like
// "0x970367…69c2" so a misuse of the short label as a wallet param does not
// silently produce a broken URL.
func looksLikeWalletAddress(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return false
	}
	if len(s) < 10 {
		return false
	}
	for _, r := range s[2:] {
		if !isHex(r) {
			return false
		}
	}
	return true
}

func isHex(r rune) bool {
	switch {
	case r >= '0' && r <= '9':
		return true
	case r >= 'a' && r <= 'f':
		return true
	case r >= 'A' && r <= 'F':
		return true
	}
	return false
}

// mdLinkEscaped renders a Telegram MarkdownV2 link [label](url). Label is
// escaped against MarkdownV2 reserved characters (Telegram requires it).
// URL is left alone because it is already a constructed Polymarket URL and
// Telegram tolerates it in the (…) link slot; we only escape `)` and `\` to
// avoid breaking out of the link.
func mdLinkEscaped(label, url string) string {
	if url == "" {
		return ""
	}
	url = strings.ReplaceAll(url, `\`, `\\`)
	url = strings.ReplaceAll(url, ")", `\)`)
	return fmt.Sprintf("[%s](%s)", telegram.EscapeMD(label), url)
}
