package telegram

import "strings"

// EscapeMD escapes a string for Telegram MarkdownV2.
// See https://core.telegram.org/bots/api#markdownv2-style
func EscapeMD(s string) string {
	// All reserved chars in MarkdownV2: _ * [ ] ( ) ~ ` > # + - = | { } . !
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
