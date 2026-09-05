package ui

import (
	"fmt"
	"html"

	"github.com/inipew/goultroid/internal/core"
)

// EscapeHTML escapes special characters for Telegram HTML mode (&, <, >).
func EscapeHTML(text string) string {
	return html.EscapeString(text)
}

// Bold returns text wrapped in <b> tags.
func Bold(text string) string {
	return "<b>" + EscapeHTML(text) + "</b>"
}

// Italic returns text wrapped in <i> tags.
func Italic(text string) string {
	return "<i>" + EscapeHTML(text) + "</i>"
}

// Underline returns text wrapped in <u> tags.
func Underline(text string) string {
	return "<u>" + EscapeHTML(text) + "</u>"
}

// Strike returns text wrapped in <s> tags.
func Strike(text string) string {
	return "<s>" + EscapeHTML(text) + "</s>"
}

// Code returns inline code wrapped in <code> tags.
func Code(text string) string {
	return "<code>" + EscapeHTML(text) + "</code>"
}

// Pre wraps text in <pre> or <pre><code class="language-..."> tags.
func Pre(text string, lang ...string) string {
	if len(lang) > 0 && lang[0] != "" {
		return `<pre><code class="language-` + EscapeHTML(lang[0]) + `">` + EscapeHTML(text) + `</code></pre>`
	}
	return "<pre>" + EscapeHTML(text) + "</pre>"
}

// Blockquote wraps text in Telegram blockquote tags.
// If expandable is true, it generates <blockquote expandable>.
func Blockquote(content string, expandable bool) string {
	if expandable {
		return "<blockquote expandable>" + content + "</blockquote>"
	}
	return "<blockquote>" + content + "</blockquote>"
}

// Badge returns a styled HTML badge representing command permission tier.
func Badge(perm core.Permission) string {
	switch perm {
	case core.PermissionOwner:
		return "👑 <b>Owner</b>"
	case core.PermissionSudo:
		return "⚡ <b>Sudo</b>"
	default:
		return "🌐 <b>Everyone</b>"
	}
}

// KeyValue formats a key-value pair as a styled bullet point.
func KeyValue(key, val string) string {
	return "• <b>" + EscapeHTML(key) + ":</b> " + val
}

// FormatBytes formats byte counts into human-readable strings (e.g. 12.5 MB).
func FormatBytes(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	const unit = 1024.0
	div, exp := int64(unit), 0
	for n := b / 1024; n >= 1024; n /= 1024 {
		div *= 1024
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
