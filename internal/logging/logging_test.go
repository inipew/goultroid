package logging

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type secretStruct struct {
	Token    string
	Password string
}

func (s secretStruct) String() string {
	return "secretStruct{token=" + s.Token + "}"
}

func TestSanitizeURL_Adversarial(t *testing.T) {
	tests := []struct {
		name           string
		raw            string
		want           string
		forbiddenParts []string
	}{
		{
			name:           "userinfo and query token",
			raw:            "https://user:password@example.com/path?token=secret",
			want:           "https://example.com/[redacted]",
			forbiddenParts: []string{"user", "password", "token", "secret", "/path"},
		},
		{
			name:           "root query api_key",
			raw:            "https://example.com/?api_key=secret",
			want:           "https://example.com/[redacted]",
			forbiddenParts: []string{"api_key", "secret"},
		},
		{
			name:           "path secret token",
			raw:            "https://example.com/download/secret-token",
			want:           "https://example.com/[redacted]",
			forbiddenParts: []string{"download", "secret-token"},
		},
		{
			name:           "fragment access_token",
			raw:            "https://example.com/#access_token=secret",
			want:           "https://example.com/[redacted]",
			forbiddenParts: []string{"access_token", "secret"},
		},
		{
			name:           "internal localhost",
			raw:            "http://127.0.0.1:8080/admin?otp=123456",
			want:           "http://[internal-host]/[redacted]",
			forbiddenParts: []string{"127.0.0.1", "admin", "otp", "123456"},
		},
		{
			name:           "internal private ip",
			raw:            "https://10.0.0.5/private/keys",
			want:           "https://[internal-host]/[redacted]",
			forbiddenParts: []string{"10.0.0.5", "private", "keys"},
		},
		{
			name:           "internal dot-internal hostname",
			raw:            "https://auth.internal/v1/session",
			want:           "https://[internal-host]/[redacted]",
			forbiddenParts: []string{"auth.internal", "session"},
		},
		{
			name:           "clean root url",
			raw:            "https://api.telegram.org",
			want:           "https://api.telegram.org",
			forbiddenParts: nil,
		},
		{
			name:           "malformed url",
			raw:            "://bad-url?token=secret",
			want:           "[redacted-url]",
			forbiddenParts: []string{"token", "secret"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeURL(tc.raw)
			if got != tc.want {
				t.Fatalf("SanitizeURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			for _, bad := range tc.forbiddenParts {
				if strings.Contains(got, bad) {
					t.Errorf("SanitizeURL(%q) leaked %q in %q", tc.raw, bad, got)
				}
			}
		})
	}
}

func TestRedactField_UnknownAndSensitive(t *testing.T) {
	s := secretStruct{Token: "tok-12345", Password: "pw"}

	anyStruct := RedactField(Any("payload", s))
	if anyStruct.Kind != KindString || strings.Contains(anyStruct.Str, "tok-12345") {
		t.Fatalf("expected unknown struct redacted, got %+v", anyStruct)
	}

	anyBytes := RedactField(Any("raw_bytes", []byte("secret-bytes")))
	if anyBytes.Kind != KindString || strings.Contains(anyBytes.Str, "secret-bytes") {
		t.Fatalf("expected []byte redacted, got %+v", anyBytes)
	}

	anyMap := RedactField(Any("headers", map[string]string{"Authorization": "Bearer abc"}))
	if anyMap.Kind != KindString || strings.Contains(anyMap.Str, "Bearer abc") {
		t.Fatalf("expected map redacted, got %+v", anyMap)
	}

	sensKey := RedactField(String("api_key", "my-secret-value"))
	if sensKey.Str != RedactedValue {
		t.Fatalf("expected sensitive key redacted, got %q", sensKey.Str)
	}

	errField := RedactField(Error(errors.New("request to https://user:pw@example.com/p?token=xyz failed with Bearer abc123")))
	if strings.Contains(errField.Str, "pw") || strings.Contains(errField.Str, "xyz") || strings.Contains(errField.Str, "abc123") {
		t.Fatalf("expected error secrets redacted, got %q", errField.Str)
	}
}

func TestRedactPanic_SafeSummaryAndBoundedStack(t *testing.T) {
	pType, summary, stack := RedactPanic(secretStruct{Token: "secret-tok"}, []byte("goroutine 1:\n/home/alice/app/secret.go:10\nhttps://example.com/?token=xyz\n"))
	if pType != "logging.secretStruct" {
		t.Fatalf("unexpected panicType %q", pType)
	}
	if strings.Contains(summary, "secret-tok") {
		t.Fatalf("panic summary leaked struct secret: %q", summary)
	}
	if strings.Contains(stack, "/home/alice") || strings.Contains(stack, "xyz") {
		t.Fatalf("panic stack leaked home path or token: %q", stack)
	}
}

func TestRedactMetadata_Allowlist(t *testing.T) {
	meta := map[string]string{
		"method": "GET",
		"url":    "https://example.com/secret?token=abc",
		"token":  "should-not-pass",
		"extra":  "not-allowlisted",
	}
	got := RedactMetadata(meta, []string{"method", "url", "token"})
	if len(got) != 2 {
		t.Fatalf("expected 2 allowlisted keys, got %+v", got)
	}
	if got["method"] != "GET" {
		t.Fatalf("unexpected method: %q", got["method"])
	}
	if strings.Contains(got["url"], "secret") || strings.Contains(got["url"], "abc") {
		t.Fatalf("metadata url leaked secret: %q", got["url"])
	}
}

func TestCorrelationContextAndClassifier(t *testing.T) {
	ctx := WithCorrelationID(context.Background(), "")
	cid := CorrelationID(ctx)
	if !strings.HasPrefix(cid, "cid-") {
		t.Fatalf("expected cid- prefix, got %q", cid)
	}
	ctx = WithCausationID(ctx, "cause-1")
	fields := ContextFields(ctx)
	if len(fields) != 2 {
		t.Fatalf("expected 2 context fields, got %d", len(fields))
	}

	cat, retryable := ClassifyError(context.DeadlineExceeded)
	if cat != CategoryCancellation || retryable {
		t.Fatalf("unexpected classification for DeadlineExceeded: %s %v", cat, retryable)
	}
	cat, retryable = ClassifyError(errors.New("FLOOD_WAIT_30"))
	if cat != CategoryTelegramRPC || !retryable {
		t.Fatalf("unexpected classification for FLOOD_WAIT: %s %v", cat, retryable)
	}
}

type typedProviderErr struct {
	cat       ErrorCategory
	retryable bool
}

func (e typedProviderErr) Error() string                          { return "typed" }
func (e typedProviderErr) LoggingCategory() (ErrorCategory, bool) { return e.cat, e.retryable }

type panickingErr struct{}

func (panickingErr) Error() string { panic("error formatting panic") }

func TestClassifyError_TypedAndSentinel(t *testing.T) {
	cat, retry := ClassifyError(typedProviderErr{cat: CategoryPersistence, retryable: true})
	if cat != CategoryPersistence || !retry {
		t.Fatalf("expected persistence/true, got %s/%v", cat, retry)
	}
	cat, retry = ClassifyError(context.Canceled)
	if cat != CategoryCancellation || retry {
		t.Fatalf("expected cancellation/false, got %s/%v", cat, retry)
	}
	// Plain error containing "timeout" without net.Error must not blindly classify as retryable network.
	cat, retry = ClassifyError(errors.New("arbitrary timeout message"))
	if cat != CategoryInternal || retry {
		t.Fatalf("expected internal/false for untyped string, got %s/%v", cat, retry)
	}
}

func TestRedactError_PanickingErrorIsolated(t *testing.T) {
	got := RedactError(panickingErr{})
	if got != "[redacted:error-panic]" {
		t.Fatalf("expected [redacted:error-panic], got %q", got)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

func TestCorrelationID_ValidationAndEntropyFallback(t *testing.T) {
	// Valid ID preserved
	ctx := WithCorrelationID(context.Background(), "req-123_abc.1")
	if got := CorrelationID(ctx); got != "req-123_abc.1" {
		t.Fatalf("expected valid ID preserved, got %q", got)
	}

	// Invalid IDs (newlines, control chars, spaces, too long) replaced with fresh cid-
	for _, bad := range []string{"", "bad\nid", "bad\x00id", "has space", strings.Repeat("a", MaxCorrelationIDLen+1)} {
		c := WithCorrelationID(context.Background(), bad)
		got := CorrelationID(c)
		if !IsValidCorrelationID(got) || got == bad {
			t.Fatalf("expected invalid correlation ID %q replaced, got %q", bad, got)
		}
	}

	// Invalid causation ID ignored
	c2 := WithCausationID(context.Background(), "bad\ncause")
	if got := CausationID(c2); got != "" {
		t.Fatalf("expected invalid causation ID ignored, got %q", got)
	}

	// Entropy failure falls back to monotonic sequence
	id1 := newCorrelationIDFrom(failingReader{})
	id2 := newCorrelationIDFrom(failingReader{})
	if !IsValidCorrelationID(id1) || !IsValidCorrelationID(id2) || id1 == id2 {
		t.Fatalf("expected distinct valid fallback IDs, got %q and %q", id1, id2)
	}
}

func TestRedactField_UnknownKindFailClosed(t *testing.T) {
	cases := []Field{
		{Key: "diagnostic", Kind: KindUnknown, Str: "token=super-secret"},
		{Key: "diagnostic", Kind: FieldKind(99), Str: "raw-secret-payload", Any: secretStruct{Token: "abc"}},
	}
	for _, tc := range cases {
		got := RedactField(tc)
		if got.Kind != KindString || got.Str != UnknownKindPlaceholder {
			t.Fatalf("expected fail-closed %q, got %+v", UnknownKindPlaceholder, got)
		}
		if strings.Contains(got.Str, "super-secret") || strings.Contains(got.Str, "raw-secret-payload") {
			t.Fatalf("unknown FieldKind leaked raw Str: %+v", got)
		}
	}
}

func TestRedactString_AdversarialSecrets(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		forbidden []string
	}{
		{"camelCase accessToken", "accessToken=abc123", []string{"abc123"}},
		{"camelCase refreshToken", "refreshToken=def456", []string{"def456"}},
		{"hyphenated x-api-key", "x-api-key=ghi789", []string{"ghi789"}},
		{"compound auth_token", "auth_token=jkl012", []string{"jkl012"}},
		{"uppercase URL", "failed HTTPS://Example.COM/p?token=secret99", []string{"secret99", "/p"}},
		{"quoted password with spaces", `password="my secret" ok`, []string{"my", "secret"}},
		{"single-quoted password", `password='my secret' ok`, []string{"my", "secret"}},
		{"unclosed quoted password", `password="my secret tail`, []string{"my", "secret", "tail"}},
		{"apikey header", "ApiKey my-header-key", []string{"my-header-key"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactString(tc.input)
			for _, bad := range tc.forbidden {
				if strings.Contains(got, bad) {
					t.Errorf("RedactString(%q) = %q, leaked %q", tc.input, got, bad)
				}
			}
		})
	}
}

func TestSanitizeURL_IPv6ZoneAndEncodedPath(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		policy    URLPolicy
		want      string
		forbidden []string
	}{
		{
			name:      "uppercase scheme",
			raw:       "HTTPS://User:Pass@Example.COM/path?token=xyz#frag",
			want:      "https://example.com/[redacted]",
			forbidden: []string{"User", "Pass", "path", "xyz", "frag"},
		},
		{
			name:      "ipv6 zone id",
			raw:       "http://[fe80::1%25eth0]:8080/admin?token=123",
			want:      "http://[internal-host]/[redacted]",
			forbidden: []string{"fe80", "eth0", "admin", "123"},
		},
		{
			name:      "ipv6 loopback",
			raw:       "http://[::1]:8080/admin",
			want:      "http://[internal-host]/[redacted]",
			forbidden: []string{"::1", "admin"},
		},
		{
			name:      "percent-encoded path default policy",
			raw:       "https://example.com/%73ecret%2Dtoken",
			want:      "https://example.com/[redacted]",
			forbidden: []string{"73ecret", "secret", "token"},
		},
		{
			name:      "percent-encoded secret segment with AllowPath",
			raw:       "https://example.com/v1/%73ecret%2Dtoken",
			policy:    URLPolicy{AllowPath: true},
			want:      "https://example.com/v1/[redacted]",
			forbidden: []string{"73ecret", "secret", "token"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeURLWithPolicy(tc.raw, tc.policy)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for _, bad := range tc.forbidden {
				if strings.Contains(got, bad) {
					t.Errorf("leaked %q in %q", bad, got)
				}
			}
		})
	}
}

func TestRedactPanic_DoesNotTrustRawStringOrError(t *testing.T) {
	pType, summary, _ := RedactPanic("user submitted private recovery phrase", nil)
	if pType != "string" || strings.Contains(summary, "recovery phrase") {
		t.Fatalf("raw panic string leaked: type=%q summary=%q", pType, summary)
	}
	pType2, summary2, _ := RedactPanic(errors.New("sensitive panic phrase"), nil)
	if !strings.Contains(pType2, "errorString") || strings.Contains(summary2, "sensitive panic phrase") {
		t.Fatalf("raw panic error leaked: type=%q summary=%q", pType2, summary2)
	}
}
