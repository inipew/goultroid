package media

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

func TestResourceGuardHeldMediaLeaseBypassesLocalSemaphore(t *testing.T) {
	guard := NewResourceGuard(1, 1)
	release, err := guard.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx := core.WithHeldResource(context.Background(), "media")
	started := time.Now()
	releaseHeld, err := guard.Acquire(ctx)
	if err != nil {
		t.Fatalf("held TaskEngine media lease should bypass local semaphore: %v", err)
	}
	releaseHeld()
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("held media lease still waited on local semaphore: %v", elapsed)
	}
}

func TestResourceGuardDirectCallerStillUsesLocalSemaphore(t *testing.T) {
	guard := NewResourceGuard(1, 1)
	release, err := guard.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := guard.Acquire(ctx); err == nil {
		release()
		t.Fatal("direct caller unexpectedly bypassed local semaphore")
	}
	release()
}
