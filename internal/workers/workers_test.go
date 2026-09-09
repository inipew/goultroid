package workers

import (
	"context"
	"errors"
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

func TestPool_StopDrainsAcceptedTasks(t *testing.T) {
	pool := NewPool("drain", 1, 4, queue.PolicyBlock)
	pool.Start(context.Background())

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondRan := make(chan struct{})
	if err := pool.Submit(context.Background(), tasks.Task{ID: "drain-1", Owner: "test", Run: func(context.Context) error {
		close(firstStarted)
		<-releaseFirst
		return nil
	}}); err != nil {
		t.Fatalf("submit first task: %v", err)
	}
	<-firstStarted
	if err := pool.Submit(context.Background(), tasks.Task{ID: "drain-2", Owner: "test", Run: func(context.Context) error {
		close(secondRan)
		return nil
	}}); err != nil {
		t.Fatalf("submit second task: %v", err)
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- pool.Stop(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for pool.running.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if pool.running.Load() {
		t.Fatal("pool did not close admission during shutdown")
	}
	close(releaseFirst)

	select {
	case <-secondRan:
	case <-time.After(time.Second):
		t.Fatal("accepted queued task was not drained")
	}
	if err := <-stopDone; err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if got := pool.Stats(); got.TasksExecuted != 2 || got.TasksSuccess != 2 {
		t.Fatalf("unexpected stats after drain: %+v", got)
	}
}

func TestPool_StopDeadlineCancelsActiveWorkAndAbandonsQueue(t *testing.T) {
	pool := NewPool("deadline", 1, 4, queue.PolicyBlock)
	pool.Start(context.Background())

	firstStarted := make(chan struct{})
	secondRan := atomic.Bool{}
	if err := pool.Submit(context.Background(), tasks.Task{ID: "deadline-1", Owner: "test", Run: func(ctx context.Context) error {
		close(firstStarted)
		<-ctx.Done()
		return ctx.Err()
	}}); err != nil {
		t.Fatalf("submit first task: %v", err)
	}
	<-firstStarted
	if err := pool.Submit(context.Background(), tasks.Task{ID: "deadline-2", Owner: "test", Run: func(context.Context) error {
		secondRan.Store(true)
		return nil
	}}); err != nil {
		t.Fatalf("submit second task: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := pool.Stop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() error = %v, want context.DeadlineExceeded", err)
	}
	if err := pool.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
	if secondRan.Load() {
		t.Fatal("queued task ran after graceful shutdown deadline")
	}
}

func TestManager_ComponentAndPools(t *testing.T) {
	mgr := NewManager()

	if mgr.Name() != "workers" {
		t.Errorf("expected name 'workers', got %s", mgr.Name())
	}
	if _, exists := mgr.Get("event"); exists {
		t.Fatal("event pool duplicates the EventBus executor and must not be provisioned")
	}
	if got := len(mgr.AllStats()); got != 4 {
		t.Fatalf("expected 4 workload pools, got %d", got)
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

func TestManager_AcceptedTaskWaitsWhenOwnerConcurrencyIsFull(t *testing.T) {
	mgr := NewManager()
	tm := tasks.NewManager()
	mgr.SetTasksManager(tm)
	mgr.SetOwnerQuota("plugin:serial", tasks.Quota{MaxConcurrent: 1, MaxQueued: 2})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = mgr.Stop(context.Background()) }()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	if err := mgr.Submit(ctx, PoolGeneral, tasks.Task{
		ID: "serial-1", Owner: "plugin:serial",
		Run: func(context.Context) error {
			close(firstStarted)
			<-releaseFirst
			return nil
		},
	}); err != nil {
		t.Fatalf("submit first task: %v", err)
	}
	<-firstStarted
	if err := mgr.Submit(ctx, PoolGeneral, tasks.Task{
		ID: "serial-2", Owner: "plugin:serial",
		Run: func(context.Context) error {
			close(secondStarted)
			return nil
		},
	}); err != nil {
		t.Fatalf("submit second task: %v", err)
	}

	select {
	case <-secondStarted:
		t.Fatal("second task exceeded owner concurrency quota")
	case <-time.After(20 * time.Millisecond):
	}
	stats, ok := mgr.OwnerTaskStats("plugin:serial")
	if !ok || stats.Running != 1 || stats.Queued != 1 || stats.Failed != 0 {
		t.Fatalf("unexpected saturated owner stats: %+v, found=%v", stats, ok)
	}

	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("accepted second task did not run after slot was released")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats, _ = mgr.OwnerTaskStats("plugin:serial")
		if stats.Completed == 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("accepted tasks were not completed: %+v", stats)
}
