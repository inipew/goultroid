package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
	"go.uber.org/zap"
)

func TestPeriodicExecutionFlowsThroughWorkerTaskLifecycle(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	engine := NewEngine(NewSQLiteRepository(db.DB), nil, nil, nil, zap.NewNop())
	workerMgr := workers.NewManager()
	taskMgr := tasks.NewManager()
	workerMgr.SetTasksManager(taskMgr)
	engine.SetWorkers(workerMgr, taskMgr)

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

	ran := make(chan struct{}, 1)
	if err := engine.RegisterPeriodicTaskOwned("periodic-owner", "worker-routed", 5*time.Millisecond, func(context.Context) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("periodic task did not execute")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats := taskMgr.Stats().Owners["periodic-owner"]
		if stats.Completed > 0 {
			if pool, ok := workerMgr.Get(workers.PoolGeneral); !ok || pool.Stats().TasksExecuted == 0 {
				t.Fatal("periodic task completed without worker pool execution accounting")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("periodic task bypassed TaskManager lifecycle: %+v", taskMgr.Stats().Owners["periodic-owner"])
}
