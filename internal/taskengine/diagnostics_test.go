package taskengine

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestPoolRuntimeStatsDiagnostics(t *testing.T) {
	engine := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"diag": {Concurrency: 2, MinConcurrency: 2, BacklogLimit: 10},
		},
		ResultCapacity: 10,
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })

	runningStarted := make(chan struct{})
	releaseTask := make(chan struct{})
	ticket, err := engine.Submit(context.Background(), tasks.WorkSpec{
		ID:         "diag-task-1",
		Pool:       "diag",
		QuotaOwner: "test",
		Handler: func(ctx context.Context) error {
			close(runningStarted)
			<-releaseTask
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-runningStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task to start")
	}

	stats, err := engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	poolStats, ok := stats.Pools["diag"]
	if !ok {
		t.Fatal("expected diag pool in stats")
	}
	if poolStats.Running != 1 {
		t.Fatalf("expected 1 running task, got %d", poolStats.Running)
	}
	if poolStats.Workers != 2 {
		t.Fatalf("expected 2 workers, got %d", poolStats.Workers)
	}
	if poolStats.Idle != poolStats.IdleWorkers {
		t.Fatalf("expected Idle == IdleWorkers (%d vs %d)", poolStats.Idle, poolStats.IdleWorkers)
	}

	close(releaseTask)
	if _, err := ticket.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	statsAfter, err := engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	poolStatsAfter := statsAfter.Pools["diag"]
	if poolStatsAfter.Running != 0 {
		t.Fatalf("expected 0 running tasks after completion, got %d", poolStatsAfter.Running)
	}
}
