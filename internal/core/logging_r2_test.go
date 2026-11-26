package core

import (
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestLoggingMiddleware_NeverLogsArgsOrSecrets(t *testing.T) {
	obsCore, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(obsCore)

	handler := LoggingMiddleware(logger)(func(*Context) error {
		return errors.New("request failed to https://user:pw@example.com/p?token=secret-url-tok")
	})

	_ = handler(&Context{
		Command:       "myxl",
		Args:          []string{"081234567890", "otp=999888"},
		CorrelationID: "cid-test-1",
	})

	entries := logs.All()
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 log entries, got %d", len(entries))
	}
	for _, entry := range entries {
		fields := entry.ContextMap()
		if _, hasArgs := fields["args"]; hasArgs {
			t.Fatalf("LoggingMiddleware must never emit raw args field: %+v", fields)
		}
		for k, v := range fields {
			s, _ := v.(string)
			for _, bad := range []string{"081234567890", "999888", "secret-url-tok", "pw"} {
				if strings.Contains(s, bad) {
					t.Errorf("field %s leaked %q in %q", k, bad, s)
				}
			}
		}
	}
}

func TestRecoveryMiddleware_RedactsPanicPayload(t *testing.T) {
	obsCore, logs := observer.New(zapcore.ErrorLevel)
	logger := zap.New(obsCore)

	handler := RecoveryMiddleware(logger)(func(*Context) error {
		panic("user submitted private recovery phrase")
	})

	err := handler(&Context{Command: "auth", CorrelationID: "cid-test-2"})
	if err == nil {
		t.Fatal("expected error from recovered panic")
	}

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 panic log entry, got %d", len(entries))
	}
	for k, v := range entries[0].ContextMap() {
		s, _ := v.(string)
		if strings.Contains(s, "recovery phrase") {
			t.Errorf("field %s leaked panic string: %q", k, s)
		}
	}
}
