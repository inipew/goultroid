package workers

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/queue"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestManagerTrySubmitRejectsFullAdmissionWithoutBlocking(t *testing.T) {
	mgr := NewManager()
	tm := tasks.NewManager()
	mgr.SetTasksManager(tm)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Stop(context.Background()) }()

	pool, ok := mgr.Get(PoolScheduler)
	if !ok {
		t.Fatal("scheduler pool missing")
	}
	for i := 0; i < cap(pool.admissions); i++ {
		pool.admissions <- struct{}{}
	}
	defer func() {
		for len(pool.admissions) > 0 {
			<-pool.admissions
		}
	}()

	err := mgr.TrySubmit(context.Background(), PoolScheduler, tasks.Task{
		ID: "try-submit-full", Owner: "scheduler-test",
		Run: func(context.Context) error { return nil },
	})
	if !errors.Is(err, queue.ErrQueueFull) {
		t.Fatalf("TrySubmit() error = %v, want queue.ErrQueueFull", err)
	}
	if _, exists := tm.GetTask("try-submit-full"); exists {
		t.Fatal("rejected non-blocking task was registered")
	}
}
