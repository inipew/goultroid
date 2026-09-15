package workers

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/queue"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestManager_TaskRemainsQueuedUntilPhysicalWorkerStarts(t *testing.T) {
	mgr := NewManager()
	tm := tasks.NewManager()
	mgr.SetTasksManager(tm)
	mgr.SetOwnerQuota("owner", tasks.Quota{MaxConcurrent: 2, MaxQueued: 4})

	pool := NewPool("single", 1, 4, queue.PolicyBlock)
	if err := mgr.AddPool(pool); err != nil {
		t.Fatalf("AddPool() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = mgr.Stop(context.Background()) }()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	if err := mgr.Submit(ctx, "single", tasks.Task{
		ID: "physical-1", Owner: "owner",
		Run: func(context.Context) error {
			close(firstStarted)
			<-releaseFirst
			return nil
		},
	}); err != nil {
		t.Fatalf("submit first task: %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first task did not start")
	}

	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	if err := mgr.Submit(ctx, "single", tasks.Task{
		ID: "physical-2", Owner: "owner",
		Run: func(context.Context) error {
			close(secondStarted)
			<-releaseSecond
			return nil
		},
	}); err != nil {
		t.Fatalf("submit second task: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		task, ok := tm.GetTask("physical-2")
		stats := pool.Stats()
		if ok && stats.QueueStats.Depth == 1 {
			if task.State != tasks.StateQueued {
				t.Fatalf("task state before physical worker start = %s, want queued", task.State)
			}
			ownerStats := tm.Stats().Owners["owner"]
			if ownerStats.Running != 1 || ownerStats.Queued != 1 {
				t.Fatalf("owner stats before physical start = %+v, want running=1 queued=1", ownerStats)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("second task was not physically queued: task=%+v ok=%v pool=%+v", task, ok, stats)
		}
		time.Sleep(time.Millisecond)
	}

	select {
	case <-secondStarted:
		t.Fatal("second task started while the only physical worker was occupied")
	default:
	}

	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second task did not start after physical worker became available")
	}

	if task, ok := tm.GetTask("physical-2"); !ok {
		t.Fatal("second task disappeared while running")
	} else if task.State != tasks.StateRunning {
		t.Fatalf("task state after physical worker start = %s, want running", task.State)
	}

	close(releaseSecond)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if tm.Stats().Owners["owner"].Completed == 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("tasks did not complete: %+v", tm.Stats().Owners["owner"])
}
