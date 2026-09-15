package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
	"go.uber.org/zap"
)

func TestScheduledManagedJobsDoNotWaitOnSameSchedulerPool(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewSQLiteRepository(db.DB)
	engine := NewEngine(repo, nil, nil, nil, zap.NewNop())
	workerMgr := workers.NewManager()
	taskMgr := tasks.NewManager()
	workerMgr.SetTasksManager(taskMgr)
	engine.SetWorkers(workerMgr, taskMgr)

	jobsMgr := jobs.NewManager(workerMgr)
	engine.SetJobsManager(jobsMgr)

	ran := make(chan string, 4)
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("same-pool-%d", i)
		if err := jobsMgr.Register(jobs.Job{
			ID: id, Owner: "managed", Pool: workers.PoolScheduler,
			Run: func(context.Context) error {
				ran <- id
				return nil
			},
		}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		if _, err := engine.ScheduleManagedJob(context.Background(), id, time.Now().Add(-time.Second), 0); err != nil {
			t.Fatalf("schedule %s: %v", id, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := workerMgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = workerMgr.Stop(context.Background()) }()
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Stop(context.Background()) }()

	seen := make(map[string]bool)
	deadline := time.After(2 * time.Second)
	for len(seen) < 4 {
		select {
		case id := <-ran:
			seen[id] = true
		case <-deadline:
			t.Fatalf("managed jobs starved in scheduler pool; completed=%d/4", len(seen))
		}
	}
}
