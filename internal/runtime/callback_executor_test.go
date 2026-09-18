package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCallbackExecutorBoundsOrphanedCallbacks(t *testing.T) {
	exec := NewCallbackExecutor(2)
	release := make(chan struct{})
	var started atomic.Int32

	runBlocked := func() error {
		started.Add(1)
		<-release
		return nil
	}

	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err := exec.Run(ctx, runBlocked)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("callback %d: expected deadline, got %v", i, err)
		}
	}
	if got := started.Load(); got != 2 {
		t.Fatalf("started=%d, want 2", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := exec.Run(ctx, runBlocked)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third callback: expected deadline waiting for capacity, got %v", err)
	}
	if got := started.Load(); got != 2 {
		t.Fatalf("third callback unexpectedly spawned; started=%d", got)
	}
	stats := exec.Stats()
	if stats.Active != 2 || stats.Peak != 2 || stats.Saturated == 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if exec.Stats().Active == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("callbacks did not release executor slots: %+v", exec.Stats())
}

func TestCallbackExecutorRecoversPanicWithStack(t *testing.T) {
	exec := NewCallbackExecutor(1)
	err := exec.Run(context.Background(), func() error {
		panic("boom")
	})
	var panicErr *CallbackPanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("expected CallbackPanicError, got %T %v", err, err)
	}
	if panicErr.Value != "boom" || len(panicErr.Stack) == 0 {
		t.Fatalf("incomplete panic error: %+v", panicErr)
	}
	if stats := exec.Stats(); stats.Active != 0 {
		t.Fatalf("panic leaked executor capacity: %+v", stats)
	}
}
