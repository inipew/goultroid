package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
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
