package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/queue"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
	"go.uber.org/zap"
)

func TestManagedJobDispatchFailsFastWhenSchedulerAdmissionIsFull(t *testing.T) {
	workerMgr := workers.NewManager()
	taskMgr := tasks.NewManager()
	workerMgr.SetTasksManager(taskMgr)
	workerMgr.SetOwnerQuota("saturator", tasks.Quota{MaxConcurrent: 1, MaxQueued: 200})
	if err := workerMgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = workerMgr.Stop(context.Background()) }()

	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	if err := workerMgr.Submit(context.Background(), workers.PoolScheduler, tasks.Task{
		ID: "saturator-running", Owner: "saturator",
		Run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	<-started

	// Fill every scheduler admission token with work that cannot advance past
	// owner concurrency. This uses only public WorkerManager behavior and
	// reproduces the saturation that used to make nested Trigger block.
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("saturator-pending-%d", i)
		if err := workerMgr.Submit(context.Background(), workers.PoolScheduler, tasks.Task{
			ID: id, Owner: "saturator", Run: func(context.Context) error { return nil },
		}); err != nil {
			t.Fatalf("fill scheduler admission %d: %v", i, err)
		}
	}

	jobsMgr := jobs.NewManager(workerMgr)
	if err := jobsMgr.Register(jobs.Job{
		ID: "same-pool-full", Owner: "managed", Pool: workers.PoolScheduler,
		Run: func(context.Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(nil, nil, nil, nil, zap.NewNop())
	engine.ctx = context.Background()
	engine.SetJobsManager(jobsMgr)

	err := engine.executeManagedJob(context.Background(), ScheduledJob{Payload: "same-pool-full"})
	if !errors.Is(err, queue.ErrQueueFull) {
		t.Fatalf("executeManagedJob() error = %v, want queue.ErrQueueFull", err)
	}
	job, ok := jobsMgr.Get("same-pool-full")
	if !ok || job.State != jobs.StateRegistered {
		t.Fatalf("failed trigger did not roll job back: %+v, found=%v", job, ok)
	}
}
