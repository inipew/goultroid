package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
)

type delayedActionTaskClient struct {
	submits atomic.Int32
}

func (c *delayedActionTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.submits.Add(1)
	if spec.Handler != nil {
		if err := spec.Handler(ctx); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
func (c *delayedActionTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (c *delayedActionTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (c *delayedActionTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func TestDelayedActionScheduler_SubmitsDueActionToTaskEngine(t *testing.T) {
	client := &delayedActionTaskClient{}
	scheduler := newDelayedActionScheduler(client)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	if err := scheduler.Start(runCtx); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}

	var ran atomic.Bool
	if err := scheduler.Schedule(context.Background(), 5*time.Millisecond, 64, func(context.Context) error {
		ran.Store(true)
		return nil
	}); err != nil {
		t.Fatalf("schedule action: %v", err)
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for !ran.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !ran.Load() {
		t.Fatal("delayed action did not execute through task client")
	}
	if got := client.submits.Load(); got != 1 {
		t.Fatalf("task submissions = %d, want 1", got)
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelStop()
	if err := scheduler.Stop(stopCtx); err != nil {
		t.Fatalf("stop scheduler: %v", err)
	}
}

func TestDelayedActionScheduler_QuiesceDropsPendingTimers(t *testing.T) {
	client := &delayedActionTaskClient{}
	scheduler := newDelayedActionScheduler(client)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	if err := scheduler.Start(runCtx); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	if err := scheduler.Schedule(context.Background(), 40*time.Millisecond, 64, func(context.Context) error {
		t.Fatal("pending delayed action must not execute after quiesce")
		return nil
	}); err != nil {
		t.Fatalf("schedule action: %v", err)
	}
	if err := scheduler.Quiesce(context.Background()); err != nil {
		t.Fatalf("quiesce scheduler: %v", err)
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelStop()
	if err := scheduler.Stop(stopCtx); err != nil {
		t.Fatalf("stop scheduler: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := client.submits.Load(); got != 0 {
		t.Fatalf("task submissions after quiesce = %d, want 0", got)
	}
}

func TestDelayedActionSchedulerRetainedByteBudget(t *testing.T) {
	client := &delayedActionTaskClient{}
	scheduler := newDelayedActionScheduler(client)
	scheduler.maxRetainedBytes = 1024
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	if err := scheduler.Start(runCtx); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.Schedule(context.Background(), time.Hour, 600, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("first delayed action: %v", err)
	}
	if got := scheduler.pendingBytes.Load(); got != 600+delayedActionOverheadBytes {
		t.Fatalf("pending bytes=%d, want %d", got, 600+delayedActionOverheadBytes)
	}
	if err := scheduler.Schedule(context.Background(), time.Hour, 1, func(context.Context) error { return nil }); !errors.Is(err, core.ErrResourceLimit) {
		t.Fatalf("expected byte-budget rejection, got %v", err)
	}
	if scheduler.byteRejections.Load() != 1 {
		t.Fatalf("byte rejection count=%d, want 1", scheduler.byteRejections.Load())
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := scheduler.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if scheduler.pending.Load() != 0 || scheduler.pendingBytes.Load() != 0 {
		t.Fatalf("shutdown retained delayed actions: pending=%d bytes=%d", scheduler.pending.Load(), scheduler.pendingBytes.Load())
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if scheduler.requests != nil || scheduler.done != nil {
		t.Fatal("shutdown retained request channel/runtime handles")
	}
}
