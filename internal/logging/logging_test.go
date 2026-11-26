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
	if cat != CategoryCancellation || !retryable {
		t.Fatalf("unexpected classification for DeadlineExceeded: %s %v", cat, retryable)
	}
	cat, retryable = ClassifyError(errors.New("FLOOD_WAIT_30"))
	if cat != CategoryTelegramRPC || !retryable {
		t.Fatalf("unexpected classification for FLOOD_WAIT: %s %v", cat, retryable)
	}
}
