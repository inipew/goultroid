package core

import (
	"context"
	"testing"
)

func TestCorrelationID(t *testing.T) {
	ctx := context.Background()

	// Empty initially
	if id := GetCorrelationID(ctx); id != "" {
		t.Fatalf("expected empty correlation ID, got %s", id)
	}

	// Auto-generate if empty passed
	ctxWithAuto := WithCorrelationID(ctx, "")
	id := GetCorrelationID(ctxWithAuto)
	if id == "" {
		t.Fatalf("expected non-empty auto-generated correlation ID")
	}

	// Explicit ID
	ctxWithExplicit := WithCorrelationID(ctx, "req-12345")
	if got := GetCorrelationID(ctxWithExplicit); got != "req-12345" {
		t.Fatalf("expected req-12345, got %s", got)
	}

	// Causation ID
	ctxWithCausation := WithCausationID(ctx, "cause-999")
	if got := GetCausationID(ctxWithCausation); got != "cause-999" {
		t.Fatalf("expected cause-999, got %s", got)
	}
}
