package core

import (
	"context"
	"testing"
	"time"
)

func TestWithDefaultTimeout_NilParent(t *testing.T) {
	ctx, cancel := WithDefaultTimeout(nil, 50*time.Millisecond)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline to be set")
	}
	if time.Until(deadline) > 60*time.Millisecond {
		t.Fatalf("deadline too far: %v", time.Until(deadline))
	}
}

func TestWithDefaultTimeout_NoDeadline(t *testing.T) {
	ctx, cancel := WithDefaultTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline to be set")
	}
	if time.Until(deadline) > 120*time.Millisecond {
		t.Fatalf("deadline too far: %v", time.Until(deadline))
	}
}

func TestWithDefaultTimeout_PreservesEarlierDeadline(t *testing.T) {
	parent, pCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer pCancel()

	ctx, cancel := WithDefaultTimeout(parent, 500*time.Millisecond)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline to be set")
	}
	// The deadline should remain the parent's ~30ms, not 500ms
	if time.Until(deadline) > 50*time.Millisecond {
		t.Fatalf("deadline was extended beyond parent: %v", time.Until(deadline))
	}
}

func TestWithDefaultTimeout_ZeroFallback(t *testing.T) {
	ctx, cancel := WithDefaultTimeout(context.Background(), 0)
	defer cancel()

	if _, ok := ctx.Deadline(); ok {
		t.Fatal("expected no deadline with zero fallback")
	}
}
