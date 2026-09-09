package workers

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/queue"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestPool_ExecuteTask(t *testing.T) {
	pool := NewPool("test", 2, 10, queue.PolicyBlock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool.Start(ctx)
	defer pool.Stop(context.Background())

	var counter atomic.Int32
	task := tasks.Task{
		ID:    "task-1",
		Owner: "plugin:test",
		Name:  "inc",
		Run: func(c context.Context) error {
			counter.Add(1)
			return nil
		},
	}

	if err := pool.Submit(ctx, task); err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	// Give workers a moment to execute
	time.Sleep(30 * time.Millisecond)

	if counter.Load() != 1 {
		t.Errorf("expected counter == 1, got %d", counter.Load())
	}

	stats := pool.Stats()
	if stats.TasksExecuted != 1 || stats.TasksSuccess != 1 {
		t.Errorf("unexpected pool stats: %+v", stats)
	}
}

func TestManager_ComponentAndPools(t *testing.T) {
	mgr := NewManager()

	if mgr.Name() != "workers" {
		t.Errorf("expected name 'workers', got %s", mgr.Name())
	}

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer mgr.Stop(ctx)

	var ran atomic.Bool
	err := mgr.Submit(ctx, PoolGeneral, tasks.Task{
		ID:    "gen-1",
		Owner: "plugin:cmd",
		Name:  "cmd",
		Run: func(c context.Context) error {
			ran.Store(true)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("submit to general pool failed: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	if !ran.Load() {
		t.Errorf("expected task in general pool to run")
	}

	h := mgr.Health(ctx)
	if h.Status != runtime.HealthHealthy {
		t.Errorf("expected healthy status, got: %s", h.Status)
	}
}
