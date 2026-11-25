package runtime

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeDiagnosticError_PreservesSafeErrors(t *testing.T) {
	safeErrors := []string{
		"connection refused",
		"context deadline exceeded",
		"database is locked",
		"dial tcp 127.0.0.1:5432: i/o timeout",
		"rpc error: code = 400 desc = PEER_ID_INVALID",
	}

	for _, tc := range safeErrors {
		got := SanitizeDiagnosticError(tc)
		if got != tc {
			t.Errorf("SanitizeDiagnosticError(%q) = %q, want %q", tc, got, tc)
		}
	}
}

func TestSanitizeDiagnosticError_Empty(t *testing.T) {
	if got := SanitizeDiagnosticError(""); got != "" {
		t.Errorf("SanitizeDiagnosticError(\"\") = %q, want \"\"", got)
	}
}

func TestSanitizeDiagnosticError_RedactsSensitiveData(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
		omits    []string
	}{
		{
			name:     "URL credentials",
			input:    "failed connecting to postgres://admin:super_secret_password@db.example.com:5432/app",
			contains: []string{"postgres://admin:[REDACTED]@db.example.com:5432/app"},
			omits:    []string{"super_secret_password"},
		},
		{
			name:     "Telegram bot token",
			input:    "failed rpc call to https://api.telegram.org/bot123456789:ABCDEF1234567890abcdef1234567890/getMe",
			contains: []string{"[REDACTED_TOKEN]"},
			omits:    []string{"123456789:ABCDEF1234567890abcdef1234567890"},
		},
		{
			name:     "Key-value secrets",
			input:    "handshake failed: token=secret_token_12345, api_hash=0123456789abcdef0123456789abcdef, session_string=1AZ3...XYZ",
			contains: []string{"token=[REDACTED]", "api_hash=[REDACTED]", "session_string=[REDACTED]"},
			omits:    []string{"secret_token_12345", "0123456789abcdef0123456789abcdef"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeDiagnosticError(tc.input)
			for _, c := range tc.contains {
				if !strings.Contains(got, c) {
					t.Errorf("expected output to contain %q, got: %s", c, got)
				}
			}
			for _, o := range tc.omits {
				if strings.Contains(got, o) {
					t.Errorf("expected output to NOT contain sensitive %q, got: %s", o, got)
				}
			}
		})
	}
}

func TestSanitizeDiagnosticError_UTF8AndLengthBounds(t *testing.T) {
	// 1. Long ASCII string: bounded to 256
	longASCII := strings.Repeat("A", 300)
	gotASCII := SanitizeDiagnosticError(longASCII)
	if len(gotASCII) != 256 {
		t.Fatalf("len(gotASCII) = %d, want 256", len(gotASCII))
	}
	if !utf8.ValidString(gotASCII) {
		t.Fatal("gotASCII is not valid UTF-8")
	}

	// 2. Multi-byte UTF-8 string: verify it never slices a multi-byte character
	// "世" is 3 bytes (0xE4, 0xB8, 0x96).
	// 85 * 3 = 255 bytes. 86 * 3 = 258 bytes.
	// Truncating to 256 should cut off at 255 bytes (85 runes), NOT 256 bytes with 1 invalid byte.
	kanjiRune := "世"
	longKanji := strings.Repeat(kanjiRune, 100)
	gotKanji := SanitizeDiagnosticError(longKanji)
	if len(gotKanji) > 256 {
		t.Fatalf("len(gotKanji) = %d > 256", len(gotKanji))
	}
	if !utf8.ValidString(gotKanji) {
		t.Fatalf("gotKanji is not valid UTF-8: %x", gotKanji)
	}
	if len(gotKanji) != 255 {
		t.Fatalf("len(gotKanji) = %d, want 255 (85 complete 3-byte kanji runes)", len(gotKanji))
	}

	// 3. 4-byte emoji string
	// "🚀" is 4 bytes (0xF0, 0x9F, 0x9A, 0x80).
	// 64 * 4 = 256 bytes.
	// Let's test 255 ASCII prefix + 1 emoji (total 259 bytes).
	// Truncation should leave only 255 ASCII bytes, dropping the incomplete emoji.
	prefix := strings.Repeat("x", 255)
	withEmoji := prefix + "🚀"
	gotEmoji := SanitizeDiagnosticError(withEmoji)
	if len(gotEmoji) != 255 {
		t.Fatalf("len(gotEmoji) = %d, want 255", len(gotEmoji))
	}
	if !utf8.ValidString(gotEmoji) {
		t.Fatal("gotEmoji is not valid UTF-8")
	}

	// 4. Corrupted UTF-8 input
	corrupted := "good prefix" + string([]byte{0xff, 0xfe, 0x80}) + "good suffix"
	gotClean := SanitizeDiagnosticError(corrupted)
	if !utf8.ValidString(gotClean) {
		t.Fatalf("gotClean is not valid UTF-8: %q", gotClean)
	}
	if !strings.Contains(gotClean, "good prefix") || !strings.Contains(gotClean, "good suffix") {
		t.Fatalf("lost valid portions of string: %q", gotClean)
	}
}
