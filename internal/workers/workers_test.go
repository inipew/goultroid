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

func TestManager_TasksManagerQuotaEnforcement(t *testing.T) {
	mgr := NewManager()
	tm := tasks.NewManager()
	mgr.SetTasksManager(tm)

	// Set a tight quota for greedy plugin: max 2 queued tasks
	mgr.SetOwnerQuota("plugin:greedy", tasks.Quota{
		MaxConcurrent: 1,
		MaxQueued:     2,
	})

	q, ok := mgr.GetOwnerQuota("plugin:greedy")
	if !ok || q.MaxQueued != 2 || q.MaxConcurrent != 1 {
		t.Fatalf("expected quota 1/2, got %+v", q)
	}

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer mgr.Stop(ctx)

	blockChan := make(chan struct{})
	defer close(blockChan)

	// Task 1 (will be running)
	err := mgr.Submit(ctx, PoolGeneral, tasks.Task{
		ID:    "t-1",
		Owner: "plugin:greedy",
		Run: func(c context.Context) error {
			<-blockChan
			return nil
		},
	})
	if err != nil {
		t.Fatalf("task 1 failed to submit: %v", err)
	}

	// Task 2 (queued)
	err = mgr.Submit(ctx, PoolGeneral, tasks.Task{
		ID:    "t-2",
		Owner: "plugin:greedy",
		Run: func(c context.Context) error {
			<-blockChan
			return nil
		},
	})
	if err != nil {
		t.Fatalf("task 2 failed to submit: %v", err)
	}

	// Task 3: Exceeds MaxQueued limit (2) -> MUST return ErrQuotaExceeded
	err = mgr.Submit(ctx, PoolGeneral, tasks.Task{
		ID:    "t-3",
		Owner: "plugin:greedy",
		Run: func(c context.Context) error {
			return nil
		},
	})
	if err == nil {
		t.Fatalf("expected ErrQuotaExceeded on task 3, got nil")
	}

	ownerStats, found := mgr.OwnerTaskStats("plugin:greedy")
	if !found {
		t.Fatalf("expected owner stats for plugin:greedy")
	}
	if ownerStats.Queued+ownerStats.Running == 0 {
		t.Errorf("expected active tasks for plugin:greedy, got %+v", ownerStats)
	}
}
