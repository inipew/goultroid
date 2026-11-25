package runtime

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxDiagnosticErrorBytes is the maximum allowed byte length for diagnostic error strings.
const maxDiagnosticErrorBytes = 256

var (
	// urlCredentialsRegex matches passwords in URLs: scheme://user:password@host
	urlCredentialsRegex = regexp.MustCompile(`(?i)(://[^:\s/]+):([^@\s/]+)@`)

	// telegramBotTokenRegex matches Telegram bot tokens: (bot)?\d{6,12}:[A-Za-z0-9_-]{20,50}
	telegramBotTokenRegex = regexp.MustCompile(`(?i)\b(?:bot)?\d{6,12}:[A-Za-z0-9_-]{20,50}\b`)

	// keyValueSecretRegex matches secret key-value assignments like token=xyz, password=xyz, secret=xyz, api_hash=xyz
	keyValueSecretRegex = regexp.MustCompile(`(?i)\b(token|password|secret|api_hash|session_string|api_key|access_token)=([^\s&,]+)`)
)

// SanitizeDiagnosticError applies targeted credential redaction, enforces UTF-8
// validity, and truncates error strings to at most 256 bytes without splitting
// multi-byte UTF-8 runes. It preserves safe, useful diagnostic error text.
func SanitizeDiagnosticError(errStr string) string {
	if errStr == "" {
		return ""
	}

	// 1. Redact credentials in URLs: scheme://user:pass@host -> scheme://user:[REDACTED]@host
	if strings.Contains(errStr, "@") && strings.Contains(errStr, "://") {
		errStr = urlCredentialsRegex.ReplaceAllString(errStr, `${1}:[REDACTED]@`)
	}

	// 2. Redact Telegram bot tokens (digits:secret)
	if strings.Contains(errStr, ":") {
		errStr = telegramBotTokenRegex.ReplaceAllString(errStr, "[REDACTED_TOKEN]")
	}

	// 3. Redact key-value secrets
	if strings.Contains(errStr, "=") {
		errStr = keyValueSecretRegex.ReplaceAllString(errStr, `${1}=[REDACTED]`)
	}

	// 4. Validate UTF-8 encoding; replace corrupted byte sequences
	if !utf8.ValidString(errStr) {
		errStr = strings.ToValidUTF8(errStr, "")
	}

	// 5. Truncate to maxDiagnosticErrorBytes ensuring valid UTF-8 rune boundary
	if len(errStr) > maxDiagnosticErrorBytes {
		errStr = errStr[:maxDiagnosticErrorBytes]
		for len(errStr) > 0 && !utf8.ValidString(errStr) {
			errStr = errStr[:len(errStr)-1]
		}
	}

	return errStr
}
