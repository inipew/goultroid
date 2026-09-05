package core

import "strings"

// EscapeHTML sanitizes text for safe inclusion in Telegram HTML messages,
// escaping &, <, and > according to Telegram Bot API HTML formatting specs.
func EscapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
