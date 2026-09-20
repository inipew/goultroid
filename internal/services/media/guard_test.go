package media

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/core"
)

func TestResourceGuardHeldMediaLeaseBypassesLocalSemaphore(t *testing.T) {
	guard := NewResourceGuard(1, 1)
	release, err := guard.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithCancel(core.WithHeldResource(context.Background(), "media"))
	cancel()
	releaseHeld, err := guard.Acquire(ctx)
	if err != nil {
		t.Fatalf("held TaskEngine media lease should bypass local semaphore even after caller cancellation: %v", err)
	}
	releaseHeld()
}

func TestResourceGuardDirectCallerStillUsesLocalSemaphore(t *testing.T) {
	guard := NewResourceGuard(1, 1)
	release, err := guard.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := guard.Acquire(ctx); err == nil {
		release()
		t.Fatal("direct caller unexpectedly bypassed local semaphore")
	}
	release()
}
