package inline

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type r2PanicHandler struct{}

func (h *r2PanicHandler) Pattern() string     { return "panicme" }
func (h *r2PanicHandler) Description() string { return "panic test" }
func (h *r2PanicHandler) HandleInline(*InlineContext) ([]InlineResult, error) {
	panic("secret-inline-panic-phrase")
}

func TestInlineEngine_NeverLogsRawQueryOrPanic(t *testing.T) {
	obsCore, logs := observer.New(zapcore.DebugLevel)
	reg := NewRegistry()
	_ = reg.Register(&r2PanicHandler{})
	eng := NewEngine(reg, zap.New(obsCore))

	// 1. Unmatched query must not log raw query text
	_ = eng.Execute(context.Background(), nil, 100, 42, "unmatched token=super-secret-query", "")

	// 2. Panic must not log raw panic string
	_ = eng.Execute(context.Background(), nil, 101, 42, "panicme secret-arg", "")

	entries := logs.All()
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 log entries, got %d", len(entries))
	}
	for _, entry := range entries {
		fields := entry.ContextMap()
		if _, hasQuery := fields["query"]; hasQuery {
			t.Fatalf("inline engine must never log raw query field: %+v", fields)
		}
		for k, v := range fields {
			s, _ := v.(string)
			for _, bad := range []string{"super-secret-query", "secret-inline-panic-phrase"} {
				if strings.Contains(s, bad) {
					t.Errorf("field %s leaked %q in %q", k, bad, s)
				}
			}
		}
	}
}
