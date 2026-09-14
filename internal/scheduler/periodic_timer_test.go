package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestPeriodicCoordinatorRunsMultipleTasksFromSharedLoop(t *testing.T) {
	c := newPeriodicCoordinator(zap.NewNop())
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
	c := newPeriodicCoordinator(zap.NewNop())
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
	c := newPeriodicCoordinator(zap.NewNop())
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
