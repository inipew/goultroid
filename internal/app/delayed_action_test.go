package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

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
	if err := scheduler.Schedule(context.Background(), 5*time.Millisecond, func(context.Context) error {
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
	if err := scheduler.Schedule(context.Background(), 40*time.Millisecond, func(context.Context) error {
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
