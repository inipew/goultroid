package app

import (
	"context"
	"errors"
	"sync"
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


func TestDelayedActionScheduler_StartIsCoordinatorLazy(t *testing.T) {
	client := &delayedActionTaskClient{}
	scheduler := newDelayedActionScheduler(client)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	if err := scheduler.Start(runCtx); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}

	scheduler.mu.Lock()
	if !scheduler.accepting {
		scheduler.mu.Unlock()
		t.Fatal("scheduler should accept work after Start")
	}
	if scheduler.requests != nil || scheduler.done != nil {
		scheduler.mu.Unlock()
		t.Fatal("idle Start eagerly allocated coordinator channels")
	}
	if scheduler.coordinatorCount != 0 || scheduler.coordinatorIdle != nil {
		scheduler.mu.Unlock()
		t.Fatalf("idle Start created coordinator state: count=%d idle=%v", scheduler.coordinatorCount, scheduler.coordinatorIdle != nil)
	}
	scheduler.mu.Unlock()

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelStop()
	if err := scheduler.Stop(stopCtx); err != nil {
		t.Fatalf("stop idle scheduler: %v", err)
	}
}

func TestDelayedActionScheduler_RetiresAndRestartsCoordinator(t *testing.T) {
	client := &delayedActionTaskClient{}
	scheduler := newDelayedActionScheduler(client)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	if err := scheduler.Start(runCtx); err != nil {
		t.Fatal(err)
	}

	var runs atomic.Int32
	scheduleAndWait := func() {
		t.Helper()
		if err := scheduler.Schedule(context.Background(), time.Millisecond, 64, func(context.Context) error {
			runs.Add(1)
			return nil
		}); err != nil {
			t.Fatalf("schedule action: %v", err)
		}
		deadline := time.Now().Add(250 * time.Millisecond)
		for runs.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if runs.Load() == 0 {
			t.Fatal("scheduled action did not run")
		}
		deadline = time.Now().Add(250 * time.Millisecond)
		for time.Now().Before(deadline) {
			scheduler.mu.Lock()
			idle := scheduler.coordinatorCount == 0 && scheduler.requests == nil && scheduler.done == nil
			scheduler.mu.Unlock()
			if idle {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("empty delayed-action coordinator did not retire")
	}

	scheduleAndWait()
	firstRuns := runs.Load()

	if err := scheduler.Schedule(context.Background(), time.Millisecond, 64, func(context.Context) error {
		runs.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("schedule after coordinator retirement: %v", err)
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for runs.Load() == firstRuns && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runs.Load() != firstRuns+1 {
		t.Fatalf("coordinator restart did not execute second action: runs=%d want=%d", runs.Load(), firstRuns+1)
	}

	deadline = time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		scheduler.mu.Lock()
		idle := scheduler.coordinatorCount == 0 && scheduler.requests == nil && scheduler.done == nil
		scheduler.mu.Unlock()
		if idle {
			break
		}
		time.Sleep(time.Millisecond)
	}
	scheduler.mu.Lock()
	idle := scheduler.coordinatorCount == 0 && scheduler.requests == nil && scheduler.done == nil
	scheduler.mu.Unlock()
	if !idle {
		t.Fatal("restarted coordinator did not retire after second action")
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelStop()
	if err := scheduler.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestDelayedActionScheduler_HealthReportsZeroIdleCoordinators(t *testing.T) {
	client := &delayedActionTaskClient{}
	scheduler := newDelayedActionScheduler(client)
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	health := scheduler.Health(context.Background())
	if health.Status == "" {
		t.Fatal("health status is empty")
	}
	if health.Details == "" {
		t.Fatal("health details are empty")
	}
	scheduler.mu.Lock()
	count := scheduler.coordinatorCount
	scheduler.mu.Unlock()
	if count != 0 {
		t.Fatalf("idle coordinator count=%d, want 0", count)
	}
	if err := scheduler.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}


func TestDelayedActionScheduler_ConcurrentLazyAdmissionDoesNotLoseActions(t *testing.T) {
	client := &delayedActionTaskClient{}
	scheduler := newDelayedActionScheduler(client)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	if err := scheduler.Start(runCtx); err != nil {
		t.Fatal(err)
	}

	const actions = 64
	var ran atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, actions)
	wg.Add(actions)
	for i := 0; i < actions; i++ {
		go func() {
			defer wg.Done()
			errs <- scheduler.Schedule(context.Background(), time.Millisecond, 32, func(context.Context) error {
				ran.Add(1)
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent schedule failed: %v", err)
		}
	}

	deadline := time.Now().Add(time.Second)
	for ran.Load() != actions && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := ran.Load(); got != actions {
		t.Fatalf("executed actions=%d, want %d", got, actions)
	}

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		scheduler.mu.Lock()
		idle := scheduler.coordinatorCount == 0 && scheduler.requests == nil && scheduler.done == nil
		scheduler.mu.Unlock()
		if idle && scheduler.pending.Load() == 0 && scheduler.pendingBytes.Load() == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	scheduler.mu.Lock()
	idle := scheduler.coordinatorCount == 0 && scheduler.requests == nil && scheduler.done == nil
	scheduler.mu.Unlock()
	if !idle {
		t.Fatal("coordinator remained resident after concurrent queue drained")
	}
	if scheduler.pending.Load() != 0 || scheduler.pendingBytes.Load() != 0 {
		t.Fatalf("retained accounting after drain: pending=%d bytes=%d", scheduler.pending.Load(), scheduler.pendingBytes.Load())
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelStop()
	if err := scheduler.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}
