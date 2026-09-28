package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
)

type rejectingDelayedActionTaskClient struct {
	submits atomic.Int32
	err     error
}

func (c *rejectingDelayedActionTaskClient) Submit(context.Context, tasks.WorkSpec) (tasks.Ticket, error) {
	c.submits.Add(1)
	return nil, c.err
}

func (c *rejectingDelayedActionTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}

func (c *rejectingDelayedActionTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int {
	return 0
}

func (c *rejectingDelayedActionTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func TestRuntimeExecutionE2_DelayedActionSubmitFailureIsObservableBestEffort(t *testing.T) {
	submitErr := errors.New("task admission unavailable")
	client := &rejectingDelayedActionTaskClient{err: submitErr}
	scheduler := newDelayedActionScheduler(client)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	if err := scheduler.Start(runCtx); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}

	var ran atomic.Bool
	if err := scheduler.Schedule(context.Background(), time.Millisecond, 64, func(context.Context) error {
		ran.Store(true)
		return nil
	}); err != nil {
		t.Fatalf("timer admission: %v", err)
	}

	deadline := time.Now().Add(250 * time.Millisecond)
	for scheduler.submitFailures.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := client.submits.Load(); got != 1 {
		t.Fatalf("task submissions = %d, want 1", got)
	}
	if ran.Load() {
		t.Fatal("action ran despite TaskEngine admission failure")
	}
	if got := scheduler.submitFailures.Load(); got != 1 {
		t.Fatalf("submit failures = %d, want 1", got)
	}
	if got := scheduler.pending.Load(); got != 0 {
		t.Fatalf("pending delayed actions = %d, want 0 after due item is consumed", got)
	}

	health := scheduler.Health(context.Background())
	if health.Status != runtime.HealthDegraded {
		t.Fatalf("health status = %s, want %s", health.Status, runtime.HealthDegraded)
	}
	if !strings.Contains(health.Details, "task submission failures: 1") {
		t.Fatalf("health details = %q, want submit failure count", health.Details)
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelStop()
	if err := scheduler.Stop(stopCtx); err != nil {
		t.Fatalf("stop scheduler: %v", err)
	}
}
