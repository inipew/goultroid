package zaplog

import (
	"errors"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/logging"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type sensitivePayload struct {
	Secret string
}

func (s sensitivePayload) String() string { return s.Secret }

func TestAdapter_RedactsBeforeZapSink(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	logger := Wrap(zap.New(core)).Named("network").With(logging.Component("http"))

	logger.Info("request failed for https://user:pass@example.com/a?token=tok123",
		logging.URL("url", "https://example.com/download/secret-token?api_key=abc#access_token=xyz"),
		logging.Any("payload", sensitivePayload{Secret: "top-secret"}),
		logging.String("msisdn", "081234567890"),
		logging.Error(errors.New("Bearer my-jwt-token")),
	)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.LoggerName != "network" {
		t.Fatalf("expected logger name network, got %q", entry.LoggerName)
	}
	if strings.Contains(entry.Message, "pass") || strings.Contains(entry.Message, "tok123") {
		t.Fatalf("message leaked secret: %q", entry.Message)
	}

	fields := entry.ContextMap()
	for k, v := range fields {
		s, _ := v.(string)
		for _, secret := range []string{"secret-token", "abc", "xyz", "top-secret", "081234567890", "my-jwt-token"} {
			if strings.Contains(s, secret) {
				t.Errorf("field %s leaked %q in %q", k, secret, s)
			}
		}
	}
	if fields["msisdn"] != logging.RedactedValue {
		t.Errorf("expected msisdn redacted, got %v", fields["msisdn"])
	}
}

func TestAdapter_NilSafetyAndSync(t *testing.T) {
	var a *Adapter
	a.Info("safe")
	if err := a.Sync(); err != nil {
		t.Fatalf("nil Adapter.Sync() returned error: %v", err)
	}

	wrapped := Wrap(nil)
	wrapped.Debug("nop")
	if err := wrapped.Sync(); err != nil {
		t.Fatalf("Wrap(nil).Sync() returned error: %v", err)
	}
}

func TestAdapter_UnknownFieldKindFailClosed(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := Wrap(zap.New(core))

	logger.Info("unknown kind test",
		logging.Field{Key: "diagnostic", Kind: logging.KindUnknown, Str: "token=super-secret"},
		logging.Field{Key: "bad_enum", Kind: logging.FieldKind(250), Str: "raw-leak"},
	)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	for k, v := range fields {
		s, _ := v.(string)
		if s != logging.UnknownKindPlaceholder {
			t.Errorf("expected %q for %s, got %q", logging.UnknownKindPlaceholder, k, s)
		}
		if strings.Contains(s, "super-secret") || strings.Contains(s, "raw-leak") {
			t.Errorf("field %s leaked raw string: %q", k, s)
		}
	}
}
