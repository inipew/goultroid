package logging

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

const (
	// RedactedValue is the canonical placeholder for redacted secrets and PII.
	RedactedValue = "[REDACTED]"
	// InternalHostPlaceholder replaces private, loopback, or internal hostnames.
	InternalHostPlaceholder = "[internal-host]"
	// UnknownKindPlaceholder is emitted when a Field has an unknown or invalid FieldKind.
	UnknownKindPlaceholder = "[redacted:unknown-kind]"
	// maxRedactedStringLen bounds error and string log payloads.
	maxRedactedStringLen = 512
	// maxStackLines caps panic stack trace lines.
	maxStackLines = 16
	// maxStackBytes caps panic stack trace bytes.
	maxStackBytes = 1536
)

// URLPolicy configures how URLs are sanitized before logging or persistence.
// By default (zero value), only scheme and safe host are preserved.
type URLPolicy struct {
	AllowPath bool
}

var (
	inlineURLPattern   = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
	authHeaderPattern  = regexp.MustCompile(`(?i)\b(bearer|basic|bot|apikey)\s+([A-Za-z0-9\-._~+/]+=*)`)
	homePathPattern    = regexp.MustCompile(`/(?:home|root)/[^\s/:]+`)
	sensitiveKeyTokens = []string{
		"token", "secret", "password", "passwd", "pwd", "otp", "pin",
		"cookie", "cookies", "authorization", "auth", "api_key", "apikey",
		"access_key", "private_key", "session", "msisdn", "phone",
		"credential", "access_hash", "bearer",
	}
)

// IsSensitiveKey reports whether a field or metadata key may carry credentials or PII.
func IsSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		return false
	}
	normalized = strings.ReplaceAll(normalized, "-", "_")
	for _, tok := range sensitiveKeyTokens {
		if strings.Contains(normalized, tok) {
			return true
		}
	}
	return false
}

// SanitizeURL conservatively redacts a URL, preserving only scheme and safe host.
func SanitizeURL(rawURL string) string {
	return SanitizeURLWithPolicy(rawURL, URLPolicy{})
}

// SanitizeURLWithPolicy redacts a URL according to policy.
// Userinfo, query parameters, fragments, and internal hosts are always stripped.
func SanitizeURLWithPolicy(rawURL string, policy URLPolicy) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "[redacted-url]"
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return scheme + "://[redacted]"
	}

	host := sanitizeHost(u.Hostname())
	if port := u.Port(); port != "" && host != InternalHostPlaceholder {
		host = net.JoinHostPort(host, port)
	}

	hasPath := (u.Path != "" && u.Path != "/") || (u.RawPath != "" && u.RawPath != "/")
	hasSensitiveTail := u.RawQuery != "" || u.Fragment != "" || u.User != nil

	if !policy.AllowPath {
		if hasPath || hasSensitiveTail {
			return scheme + "://" + host + "/[redacted]"
		}
		return scheme + "://" + host
	}

	safePath := sanitizeURLPath(u.Path)
	if hasSensitiveTail {
		if safePath == "" || safePath == "/" {
			return scheme + "://" + host + "/[redacted]"
		}
		return scheme + "://" + host + safePath + "?[redacted]"
	}
	return scheme + "://" + host + safePath
}

func sanitizeHost(host string) string {
	lower := strings.ToLower(strings.TrimSpace(host))
	if lower == "" {
		return InternalHostPlaceholder
	}
	if lower == "localhost" || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".localhost") {
		return InternalHostPlaceholder
	}
	// Strip IPv6 zone identifier before parsing IP; any zoned address is link/interface-local.
	if zoneIdx := strings.IndexByte(lower, '%'); zoneIdx != -1 {
		return InternalHostPlaceholder
	}
	if ip := net.ParseIP(lower); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return InternalHostPlaceholder
		}
	}
	return lower
}

func sanitizeURLPath(p string) string {
	if p == "" || p == "/" {
		return ""
	}
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if seg == "" {
			continue
		}
		decoded := seg
		if unescaped, err := url.PathUnescape(seg); err == nil {
			decoded = unescaped
		}
		lower := strings.ToLower(decoded)
		if IsSensitiveKey(lower) || len(decoded) >= 24 {
			parts[i] = "[redacted]"
		}
	}
	return strings.Join(parts, "/")
}

// RedactString scrubs URLs, authorization headers, sensitive key-value pairs, and home paths.
func RedactString(s string) string {
	if s == "" {
		return ""
	}
	out := redactURLsInString(s)
	out = redactAuthHeaders(out)
	out = redactKeyValueSecrets(out)
	out = homePathPattern.ReplaceAllString(out, "/[redacted-home]")
	if len(out) > maxRedactedStringLen {
		return out[:maxRedactedStringLen] + "...[truncated]"
	}
	return out
}

func redactURLsInString(s string) string {
	return inlineURLPattern.ReplaceAllStringFunc(s, func(match string) string {
		return SanitizeURL(match)
	})
}

func redactAuthHeaders(s string) string {
	return authHeaderPattern.ReplaceAllString(s, "$1 "+RedactedValue)
}

// redactKeyValueSecrets parses key=value and key:value pairs (including quoted values
// with spaces, camelCase keys, snake_case keys, and hyphenated keys) and redacts
// values whose key matches IsSensitiveKey.
func redactKeyValueSecrets(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if !isIdentStart(s[i]) {
			b.WriteByte(s[i])
			i++
			continue
		}
		keyStart := i
		for i < len(s) && isIdentChar(s[i]) {
			i++
		}
		key := s[keyStart:i]

		// Optional whitespace before '=' or ':' (excluding "://")
		sepPos := i
		for sepPos < len(s) && (s[sepPos] == ' ' || s[sepPos] == '\t') {
			sepPos++
		}
		if sepPos >= len(s) || (s[sepPos] != '=' && s[sepPos] != ':') {
			b.WriteString(key)
			continue
		}
		if s[sepPos] == ':' && sepPos+2 < len(s) && s[sepPos+1] == '/' && s[sepPos+2] == '/' {
			b.WriteString(key)
			continue
		}
		if !IsSensitiveKey(key) {
			b.WriteString(key)
			continue
		}

		// Write key and separator + any whitespace between key and sep/value.
		b.WriteString(s[keyStart : sepPos+1])
		valStart := sepPos + 1
		for valStart < len(s) && (s[valStart] == ' ' || s[valStart] == '\t') {
			b.WriteByte(s[valStart])
			valStart++
		}
		if valStart >= len(s) {
			i = valStart
			continue
		}

		if s[valStart] == '"' || s[valStart] == '\'' {
			quote := s[valStart]
			b.WriteByte(quote)
			b.WriteString(RedactedValue)
			j := valStart + 1
			for j < len(s) {
				if s[j] == '\\' && j+1 < len(s) {
					j += 2
					continue
				}
				if s[j] == quote {
					b.WriteByte(quote)
					j++
					break
				}
				j++
			}
			i = j
			continue
		}

		j := valStart
		for j < len(s) && !isValueDelimiter(s[j]) {
			j++
		}
		b.WriteString(RedactedValue)
		i = j
	}
	return b.String()
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '-'
}

func isValueDelimiter(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' || c == '&' || c == ';'
}

// RedactError returns a bounded, redacted error summary.
// If err.Error() panics, it recovers safely and returns a placeholder.
func RedactError(err error) (out string) {
	if err == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			out = "[redacted:error-panic]"
		}
	}()
	return RedactString(err.Error())
}

// RedactPanic returns a safe panic type, redacted placeholder summary, and bounded sanitized stack.
// Raw panic strings or error payloads are never emitted directly by default.
func RedactPanic(recoverVal any, stack []byte) (panicType string, summary string, safeStack string) {
	panicType = fmt.Sprintf("%T", recoverVal)
	if recoverVal != nil {
		summary = fmt.Sprintf("[redacted:%s]", panicType)
	}

	if len(stack) > 0 {
		raw := string(stack)
		if len(raw) > maxStackBytes {
			raw = raw[:maxStackBytes]
		}
		lines := strings.Split(raw, "\n")
		if len(lines) > maxStackLines {
			lines = lines[:maxStackLines]
		}
		for i, line := range lines {
			lines[i] = RedactString(line)
		}
		safeStack = strings.Join(lines, "\n")
	}
	return panicType, summary, safeStack
}

// RedactMetadata returns a new map containing only allowlisted, non-sensitive keys.
func RedactMetadata(meta map[string]string, allowedKeys []string) map[string]string {
	if len(meta) == 0 || len(allowedKeys) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(allowedKeys))
	for _, k := range allowedKeys {
		allowed[k] = struct{}{}
	}
	var out map[string]string
	for k, v := range meta {
		if _, ok := allowed[k]; !ok {
			continue
		}
		if IsSensitiveKey(k) {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(allowedKeys))
		}
		if strings.EqualFold(k, "url") || strings.HasSuffix(strings.ToLower(k), "_url") {
			out[k] = SanitizeURL(v)
		} else {
			out[k] = RedactString(v)
		}
	}
	return out
}

// RedactField enforces fail-closed redaction and unknown-field safety before sink encoding.
func RedactField(f Field) Field {
	if f.Key == "" {
		return f
	}
	if IsSensitiveKey(f.Key) && f.Kind != KindMetadata {
		return Field{Key: f.Key, Kind: KindString, Str: RedactedValue}
	}

	switch f.Kind {
	case KindString:
		f.Str = RedactString(f.Str)
		return f
	case KindInt64, KindUint64, KindFloat64, KindBool, KindDuration, KindTime:
		return f
	case KindURL:
		policy, _ := f.Any.(URLPolicy)
		return Field{Key: f.Key, Kind: KindString, Str: SanitizeURLWithPolicy(f.Str, policy)}
	case KindError:
		err, _ := f.Any.(error)
		return Field{Key: f.Key, Kind: KindString, Str: RedactError(err)}
	case KindPanic:
		payload, _ := f.Any.(panicPayload)
		panicType, summary, safeStack := RedactPanic(payload.recoverVal, payload.stack)
		return Field{
			Key:  f.Key,
			Kind: KindPanic,
			Str:  panicType,
			Any:  RedactedPanic{Type: panicType, Summary: summary, Stack: safeStack},
		}
	case KindMetadata:
		payload, _ := f.Any.(metadataPayload)
		return Field{
			Key:  f.Key,
			Kind: KindMetadata,
			Any:  RedactMetadata(payload.values, payload.allowedKeys),
		}
	case KindAny:
		return redactUnknownField(f)
	default:
		// Fail-closed: unknown or invalid FieldKind must never pass raw Str or Any through.
		return Field{Key: f.Key, Kind: KindString, Str: UnknownKindPlaceholder}
	}
}

// RedactedPanic holds the sanitized representation of a recovered panic.
type RedactedPanic struct {
	Type    string
	Summary string
	Stack   string
}

func redactUnknownField(f Field) Field {
	switch v := f.Any.(type) {
	case nil:
		return Field{Key: f.Key, Kind: KindString, Str: "<nil>"}
	case string:
		return Field{Key: f.Key, Kind: KindString, Str: RedactString(v)}
	case bool:
		return Bool(f.Key, v)
	case int:
		return Int(f.Key, v)
	case int64:
		return Int64(f.Key, v)
	case uint64:
		return Uint64(f.Key, v)
	case float64:
		return Float64(f.Key, v)
	case error:
		return Field{Key: f.Key, Kind: KindString, Str: RedactError(v)}
	default:
		// Unknown structs, maps, slices, []byte, and fmt.Stringers are never
		// assumed safe for automatic formatting.
		return Field{Key: f.Key, Kind: KindString, Str: fmt.Sprintf("[redacted:%T]", v)}
	}
}
