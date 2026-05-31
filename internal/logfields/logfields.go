// Package logfields provides tiny formatting helpers for short, audit-safe
// log values. No allocations beyond strings.Builder; no secret-handling
// logic — callers must never pass tokens/passwords here.
package logfields

import "strings"

// Short truncates a long opaque id (wallet/tx/token) to head+tail with
// an ellipsis. Returns the input unchanged if it's already short.
func Short(s string) string {
	if len(s) <= 14 {
		return s
	}
	return s[:6] + "…" + s[len(s)-4:]
}

// Title truncates a free-form title to maxLen runes (default 80) with an
// ellipsis suffix.
func Title(s string) string { return TruncRunes(s, 80) }

// TruncRunes truncates a UTF-8 string by rune count.
func TruncRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	b.WriteString(string(r[:n]))
	b.WriteString("…")
	return b.String()
}
