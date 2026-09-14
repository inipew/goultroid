package workers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/queue"
	"github.com/inipew/goultroid/internal/tasks"
)

func regressionManager(t *testing.T, capacity int) (*Manager, *tasks.Manager, *Pool) {
	t.Helper()
	m := NewManager()
	p := NewPool("regression", 1, capacity, queue.PolicyBlock)
	if err := m.AddPool(p); err != nil {
		t.Fatal(err)
	}
	tm := tasks.NewManager()
	m.SetTasksManager(tm)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return m, tm, p
}

func awaitAdmissionCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("admission condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagerPanicReleasesOwnerAndReportsCompletion(t *testing.T) {
	m, tm, p := regressionManager(t, 4)
	tm.SetOwnerQuota("owner", tasks.Quota{MaxConcurrent: 1, MaxQueued: 4})
	result := make(chan error, 1)
	if err := m.Submit(context.Background(), p.Name(), tasks.Task{
		ID: "panic", Owner: "owner",
		Run:        func(context.Context) error { panic("boom") },
		OnComplete: func(err error) { result <- err },
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("panic result = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("panic did not report completion")
	}
	if _, ok := tm.GetTask("panic"); ok {
		t.Fatal("panicked task remains tracked")
	}
	if err := m.Submit(context.Background(), p.Name(), tasks.Task{
		ID: "next", Owner: "owner", Run: func(context.Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	awaitAdmissionCondition(t, func() bool { return tm.Stats().TotalCompleted == 1 })
	if stats := tm.Stats(); stats.TotalRunning != 0 || stats.TotalFailed != 1 {
		t.Fatalf("terminal accounting = %+v", stats)
	}
}

func TestManagerCancellationWakesAdmission(t *testing.T) {
	for _, fullQueue := range []bool{false, true} {
		for _, parentCancellation := range []bool{false, true} {
			t.Run(fmt.Sprintf("full=%t/parent=%t", fullQueue, parentCancellation), func(t *testing.T) {
				m, tm, p := regressionManager(t, 1)
				tm.SetOwnerQuota("owner", tasks.Quota{MaxConcurrent: 1, MaxQueued: 4})
				started, release := make(chan struct{}), make(chan struct{})
				defer close(release)
				if err := m.Submit(context.Background(), p.Name(), tasks.Task{
					ID: "holder", Owner: "owner", Run: func(context.Context) error {
						close(started)
						<-release
						return nil
					},
				}); err != nil {
					t.Fatal(err)
				}
				<-started
				if fullQueue {
					if err := m.Submit(context.Background(), p.Name(), tasks.Task{
						ID: "queued", Owner: "other", Run: func(context.Context) error { return nil },
					}); err != nil {
						t.Fatal(err)
					}
					awaitAdmissionCondition(t, func() bool { return p.queue.Depth() == 1 })
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				if err := m.Submit(ctx, p.Name(), tasks.Task{
					ID: "cancel", Owner: "owner", Run: func(context.Context) error {
						return errors.New("cancelled task must not execute")
					}, OnComplete: func(err error) { result <- err },
				}); err != nil {
					t.Fatal(err)
				}
				// The controller has no input or completion events until we release
				// the holder. Cancellation alone must release its admission token.
				if parentCancellation {
					cancel()
				} else {
					tm.Cancel("cancel")
				}
				select {
				case err := <-result:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("completion = %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("cancellation did not wake admission")
				}
				if len(p.admissions) != 0 {
					t.Fatal("cancelled task retained admission token")
				}
			})
		}
	}
}

func TestManagerTrySubmitDoesNotWaitOnSamePool(t *testing.T) {
	m, _, p := regressionManager(t, 1)
	started, submit := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	if err := m.Submit(context.Background(), p.Name(), tasks.Task{
		ID: "outer", Owner: "outer", Run: func(ctx context.Context) error {
			close(started)
			select {
			case <-submit:
			case <-ctx.Done():
				return ctx.Err()
			}
			result <- m.TrySubmit(ctx, p.Name(), tasks.Task{
				ID: "nested", Owner: "nested", Run: func(context.Context) error { return nil },
			})
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	for i := 0; i < 2; i++ {
		if err := m.Submit(context.Background(), p.Name(), tasks.Task{
			ID: fmt.Sprintf("fill-%d", i), Owner: "fill", Run: func(context.Context) error { return nil },
		}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			awaitAdmissionCondition(t, func() bool { return p.queue.Depth() == 1 })
		}
	}
	close(submit)
	select {
	case err := <-result:
		if !errors.Is(err, queue.ErrQueueFull) {
			t.Fatalf("nested submission = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("nested submission blocked its own physical worker")
	}
}

func TestManagerAdmissionProgressesAcrossOwnerSlotChanges(t *testing.T) {
	m, tm, p := regressionManager(t, 256)
	tm.SetOwnerQuota("owner", tasks.Quota{MaxConcurrent: 1, MaxQueued: 512})
	for i := 0; i < 256; i++ {
		if err := m.Submit(context.Background(), p.Name(), tasks.Task{
			ID: fmt.Sprintf("burst-%d", i), Owner: "owner", Run: func(context.Context) error { return nil },
		}); err != nil {
			t.Fatal(err)
		}
	}
	// No further submissions or quota updates may be needed to wake the loop.
	awaitAdmissionCondition(t, func() bool { return tm.Stats().TotalCompleted == 256 })
}

func TestManagerPoolCancellationFinalizesQueuedTasks(t *testing.T) {
	m, tm, p := regressionManager(t, 4)
	started := make(chan struct{})
	if err := m.Submit(context.Background(), p.Name(), tasks.Task{
		ID: "holder", Owner: "holder", Run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	result := make(chan error, 1)
	if err := m.Submit(context.Background(), p.Name(), tasks.Task{
		ID: "abandoned", Owner: "queued", Run: func(context.Context) error {
			return errors.New("abandoned task must not execute")
		}, OnComplete: func(err error) { result <- err },
	}); err != nil {
		t.Fatal(err)
	}
	awaitAdmissionCondition(t, func() bool { return p.queue.Depth() == 1 })
	p.cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("abandoned result = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("abandoned task was not finalized")
	}
	awaitAdmissionCondition(t, func() bool { return tm.Stats().TotalCancelled == 2 })
	if stats := tm.Stats(); stats.TotalQueued != 0 || stats.TotalRunning != 0 {
		t.Fatalf("abandoned tasks retained quota: %+v", stats)
	}
}
