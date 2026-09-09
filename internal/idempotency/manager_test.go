package idempotency

import (
	"context"
	"testing"
	"time"
)

func TestIdempotencyManager_CheckAndSet(t *testing.T) {
	mgr := NewManager(10 * time.Millisecond)
	defer mgr.Close()

	ctx := context.Background()
	key := "update:12345"

	// 1. First execution should succeed (not duplicate)
	isNew, err := mgr.CheckAndSet(ctx, key, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Errorf("expected first call to return isNew=true")
	}

	if !mgr.IsProcessed(key) {
		t.Errorf("expected key to be marked processed")
	}

	// 2. Immediate second execution should be identified as duplicate
	isNew, err = mgr.CheckAndSet(ctx, key, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isNew {
		t.Errorf("expected second call to return isNew=false (duplicate)")
	}

	// 3. After TTL expires, key can be reprocessed
	time.Sleep(60 * time.Millisecond)
	if mgr.IsProcessed(key) {
		t.Errorf("expected key to be expired after TTL")
	}

	isNew, err = mgr.CheckAndSet(ctx, key, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Errorf("expected call after expiration to return isNew=true")
	}
}
