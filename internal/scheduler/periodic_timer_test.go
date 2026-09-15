package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
	"go.uber.org/zap"
)

type testPeriodicSubmitter struct{}

func (testPeriodicSubmitter) TrySubmit(ctx context.Context, _ string, task tasks.Task) error {
	go func() { _ = task.Execute(ctx) }()
	return nil
}

func newTestPeriodicCoordinator() *periodicCoordinator {
	c := newPeriodicCoordinator(zap.NewNop())
	c.SetSubmitter(testPeriodicSubmitter{})
	return c
}

func TestPeriodicCoordinatorRunsMultipleTasksFromSharedLoop(t *testing.T) {
	c := newTestPeriodicCoordinator()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		if err := c.Stop(stopCtx); err != nil {
			t.Fatal(err)
		}
	}()

	var a, b atomic.Int32
	if err := c.Register("a", 10*time.Millisecond, PeriodicTaskOptions{Owner: "x"}, func(context.Context) error {
		a.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Register("b", 15*time.Millisecond, PeriodicTaskOptions{Owner: "x"}, func(context.Context) error {
		b.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(time.Second)
	for a.Load() < 2 || b.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("periodic executions did not advance: a=%d b=%d", a.Load(), b.Load())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestPeriodicCoordinatorPreventsSameTaskOverlap(t *testing.T) {
	c := newTestPeriodicCoordinator()
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := c.Stop(stopCtx); err != nil {
			t.Fatal(err)
		}
	}()

	var running, maxRunning atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	if err := c.Register("slow", 5*time.Millisecond, PeriodicTaskOptions{}, func(context.Context) error {
		n := running.Add(1)
		for {
			old := maxRunning.Load()
			if n <= old || maxRunning.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		running.Add(-1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}
	time.Sleep(30 * time.Millisecond)
	if got := maxRunning.Load(); got != 1 {
		t.Fatalf("same task overlapped: max running=%d", got)
	}
	close(release)
}

func TestPeriodicCoordinatorUnregisterCancelsActiveRun(t *testing.T) {
	c := newTestPeriodicCoordinator()
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	started := make(chan struct{})
	stopped := make(chan struct{})
	if err := c.Register("cancel", time.Millisecond, PeriodicTaskOptions{}, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}
	if err := c.Unregister("cancel"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("active run was not cancelled")
	}
}

func TestPeriodicCoordinatorReregisterCancelsPreviousGeneration(t *testing.T) {
	c := newTestPeriodicCoordinator()
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	oldStarted := make(chan struct{})
	oldStopped := make(chan struct{})
	if err := c.Register("replace", time.Millisecond, PeriodicTaskOptions{}, func(ctx context.Context) error {
		close(oldStarted)
		<-ctx.Done()
		close(oldStopped)
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old generation did not start")
	}

	var replacementRuns atomic.Int32
	if err := c.Register("replace", 5*time.Millisecond, PeriodicTaskOptions{}, func(context.Context) error {
		replacementRuns.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldStopped:
	case <-time.After(time.Second):
		t.Fatal("re-register did not cancel previous generation")
	}

	deadline := time.After(time.Second)
	for replacementRuns.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("replacement generation did not run")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestPeriodicCoordinator_MultiOwnerSameName(t *testing.T) {
	c := newTestPeriodicCoordinator()
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	var runA, runB atomic.Int32
	// Two different owners registering the same task name "sync"
	if err := c.Register("sync", 10*time.Millisecond, PeriodicTaskOptions{Owner: "pluginA"}, func(context.Context) error {
		runA.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("register pluginA/sync failed: %v", err)
	}
	if err := c.Register("sync", 10*time.Millisecond, PeriodicTaskOptions{Owner: "pluginB"}, func(context.Context) error {
		runB.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("register pluginB/sync failed: %v", err)
	}

	deadline := time.After(time.Second)
	for runA.Load() == 0 || runB.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("tasks did not run concurrently: runA=%d, runB=%d", runA.Load(), runB.Load())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Unregister pluginA should NOT affect pluginB
	if err := c.UnregisterOwned("pluginA", "sync"); err != nil {
		t.Fatalf("unregister owned failed: %v", err)
	}

	c.mu.Lock()
	_, hasA := c.entries["pluginA/sync"]
	_, hasB := c.entries["pluginB/sync"]
	c.mu.Unlock()

	if hasA {
		t.Errorf("expected pluginA/sync to be removed")
	}
	if !hasB {
		t.Errorf("expected pluginB/sync to remain registered")
	}
}

type flappySubmitter struct {
	mu           sync.Mutex
	failAttempts int
	captured     []tasks.Task
}

func (s *flappySubmitter) TrySubmit(ctx context.Context, _ string, task tasks.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAttempts > 0 {
		s.failAttempts--
		return workers.ErrNoExecutionCapacity
	}
	s.captured = append(s.captured, task)
	return nil
}

func TestPeriodicCoordinator_SubmitSaturationDoesNotBurnRetryBudget(t *testing.T) {
	c := newPeriodicCoordinator(zap.NewNop())
	sub := &flappySubmitter{failAttempts: 3}
	c.SetSubmitter(sub)

	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	// Task with MaxAttempts: 2
	runs := 0
	if err := c.Register("saturated_task", 10*time.Millisecond, PeriodicTaskOptions{
		Owner:       "test",
		MaxAttempts: 2,
		RetryDelay:  10 * time.Millisecond,
	}, func(ctx context.Context) error {
		runs++
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Wait for the 3 failed submission attempts to pass and the 4th to succeed
	deadline := time.After(2 * time.Second)
	for {
		sub.mu.Lock()
		count := len(sub.captured)
		sub.mu.Unlock()
		if count > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for submitter to accept task after saturation")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Execute the captured task
	sub.mu.Lock()
	task := sub.captured[0]
	sub.mu.Unlock()

	if err := task.Execute(context.Background()); err != nil {
		t.Fatalf("task execution failed: %v", err)
	}

	// Check snapshots: Failures should be 3 (from saturation), Runs should be 1
	snapshots := c.Snapshots()
	if len(snapshots) == 0 {
		t.Fatal("no snapshots found")
	}
	snap := snapshots[0]
	if snap.Failures != 3 {
		t.Errorf("expected 3 failures from saturation, got: %d", snap.Failures)
	}
	if snap.Runs != 1 {
		t.Errorf("expected 1 successful run, got: %d", snap.Runs)
	}
}
