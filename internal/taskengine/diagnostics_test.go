package taskengine

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestSnapshotPoolRuntimeStatsSeparatesLifecycleStates(t *testing.T) {
	engine := NewEngine(Config{Pools: map[tasks.PoolID]PoolEngineConfig{
		"diag": {Concurrency: 3, MinConcurrency: 1, BacklogLimit: 10, PayloadBudget: 1024},
	}, ResultCapacity: 10})

	engine.workerRunning["diag"] = []bool{true, true, true}
	engine.idleSlots["diag"] = []int{2}
	engine.registry["running"] = &taskRecord{
		spec:  tasks.WorkSpec{ID: "running", Pool: "diag"},
		state: tasks.StateRunning,
	}
	engine.registry["dispatching"] = &taskRecord{
		spec:  tasks.WorkSpec{ID: "dispatching", Pool: "diag"},
		state: tasks.StateDispatching,
	}
	engine.adm.Enqueue(&admission.QueueEntry{
		Spec:        tasks.WorkSpec{ID: "waiting", Pool: "diag", QuotaOwner: "owner", Class: tasks.PriorityNormal},
		EnqueuedAt:  time.Now(),
		PayloadSize: 17,
	})

	stats := engine.snapshotPoolRuntimeStats()["diag"]
	if stats.Workers != 3 || stats.Running != 1 || stats.Dispatching != 1 || stats.Idle != 1 || stats.Waiting != 1 {
		t.Fatalf("unexpected pool lifecycle stats: %+v", stats)
	}
	if stats.Workers != stats.Running+stats.Dispatching+stats.Idle {
		t.Fatalf("worker lifecycle accounting mismatch: %+v", stats)
	}
	if stats.IdleWorkers != stats.Idle {
		t.Fatalf("legacy IdleWorkers=%d must mirror Idle=%d", stats.IdleWorkers, stats.Idle)
	}
	if stats.WaitingBytes != 17 {
		t.Fatalf("WaitingBytes=%d, want 17", stats.WaitingBytes)
	}
}

func TestPoolRuntimeStatsTrackRunningWaitingAndIdle(t *testing.T) {
	engine := NewEngine(Config{Pools: map[tasks.PoolID]PoolEngineConfig{
		"diag": {Concurrency: 1, MinConcurrency: 1, IdleTimeout: time.Minute, BacklogLimit: 10},
	}, ResultCapacity: 10})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = engine.Stop(ctx)
	})

	started := make(chan struct{}, 2)
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})

	first, err := engine.Submit(context.Background(), tasks.WorkSpec{
		ID: "diag-first", QuotaOwner: "owner", Pool: "diag",
		Handler: func(ctx context.Context) error {
			started <- struct{}{}
			select {
			case <-releaseFirst:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first task did not start")
	}

	second, err := engine.Submit(context.Background(), tasks.WorkSpec{
		ID: "diag-second", QuotaOwner: "owner", Pool: "diag",
		Handler: func(ctx context.Context) error {
			started <- struct{}{}
			select {
			case <-releaseSecond:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	waitForDetailedPoolStats(t, engine, "diag", time.Second, func(stats PoolRuntimeStats) bool {
		return stats.Workers == 1 && stats.Running == 1 && stats.Dispatching == 0 && stats.Idle == 0 && stats.Waiting == 1
	})

	close(releaseFirst)
	if _, err := first.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("second task did not start")
	}
	waitForDetailedPoolStats(t, engine, "diag", time.Second, func(stats PoolRuntimeStats) bool {
		return stats.Workers == 1 && stats.Running == 1 && stats.Dispatching == 0 && stats.Idle == 0 && stats.Waiting == 0
	})

	close(releaseSecond)
	if _, err := second.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForDetailedPoolStats(t, engine, "diag", time.Second, func(stats PoolRuntimeStats) bool {
		return stats.Workers == 1 && stats.Running == 0 && stats.Dispatching == 0 && stats.Idle == 1 && stats.Waiting == 0
	})
}

func waitForDetailedPoolStats(t *testing.T, engine *Engine, pool tasks.PoolID, timeout time.Duration, ready func(PoolRuntimeStats) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		stats, err := engine.Stats(ctx)
		cancel()
		if err == nil {
			poolStats := stats.Pools[pool]
			if poolStats.Workers != poolStats.Running+poolStats.Dispatching+poolStats.Idle {
				t.Fatalf("worker lifecycle accounting mismatch: %+v", poolStats)
			}
			if ready(poolStats) {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	stats, _ := engine.Stats(context.Background())
	t.Fatalf("pool %s did not reach expected state; got %+v", pool, stats.Pools[pool])
}
