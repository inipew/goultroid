package taskengine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestAdaptivePoolScalesAndRetires(t *testing.T) {
	engine := NewEngine(Config{Pools: map[tasks.PoolID]PoolEngineConfig{
		"adaptive": {Concurrency: 3, MinConcurrency: 1, IdleTimeout: 30 * time.Millisecond, BacklogLimit: 10},
	}, ResultCapacity: 10})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })

	release := make(chan struct{})
	for index := 0; index < 3; index++ {
		_, err := engine.Submit(context.Background(), tasks.WorkSpec{
			ID: tasks.TaskID(fmt.Sprintf("adaptive-%d", index)), QuotaOwner: "owner",
			Pool: "adaptive", Handler: func(context.Context) error { <-release; return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	waitPoolWorkers(t, engine, "adaptive", 3, time.Second)
	close(release)
	waitPoolWorkers(t, engine, "adaptive", 1, 2*time.Second)
}

func TestLivePoolAndResourceConfiguration(t *testing.T) {
	engine := NewEngine(Config{Pools: map[tasks.PoolID]PoolEngineConfig{
		"adaptive": {Concurrency: 3, MinConcurrency: 1, IdleTimeout: time.Second, BacklogLimit: 10},
	}, ResultCapacity: 10, ResourceCapacities: map[string]int64{"process": 1}})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })
	if err := engine.ConfigurePool(context.Background(), "adaptive", PoolEngineConfig{Concurrency: 2, MinConcurrency: 2, IdleTimeout: time.Second, BacklogLimit: 4, PayloadBudget: 1024}); err != nil {
		t.Fatal(err)
	}
	waitPoolWorkers(t, engine, "adaptive", 2, time.Second)
	if err := engine.SetResourceCapacity(context.Background(), "process", 3); err != nil {
		t.Fatal(err)
	}
	stats, err := engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pools["adaptive"].MaxWorkers != 2 || stats.Resources["process"].Capacity != 3 {
		t.Fatalf("unexpected live stats: %+v", stats)
	}
	if err := engine.ConfigurePool(context.Background(), "adaptive", PoolEngineConfig{Concurrency: 4, MinConcurrency: 1}); err == nil {
		t.Fatal("live configuration exceeded startup hard maximum")
	}
}

func TestLiveIdleTimeoutReconfiguration(t *testing.T) {
	engine := NewEngine(Config{Pools: map[tasks.PoolID]PoolEngineConfig{
		"adaptive": {Concurrency: 3, MinConcurrency: 1, IdleTimeout: 10 * time.Second, BacklogLimit: 10},
	}, ResultCapacity: 10})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })

	release := make(chan struct{})
	for index := 0; index < 3; index++ {
		_, err := engine.Submit(context.Background(), tasks.WorkSpec{
			ID: tasks.TaskID(fmt.Sprintf("live-idle-%d", index)), QuotaOwner: "owner",
			Pool: "adaptive", Handler: func(context.Context) error { <-release; return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	waitPoolWorkers(t, engine, "adaptive", 3, time.Second)
	close(release)

	time.Sleep(20 * time.Millisecond)

	stats, err := engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pools["adaptive"].Workers != 3 {
		t.Fatalf("expected 3 workers while idle under 10s timeout, got %d", stats.Pools["adaptive"].Workers)
	}

	if err := engine.ConfigurePool(context.Background(), "adaptive", PoolEngineConfig{
		Concurrency: 3, MinConcurrency: 1, IdleTimeout: 30 * time.Millisecond, BacklogLimit: 10,
	}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	waitPoolWorkers(t, engine, "adaptive", 1, 500*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("retirement took too long (%v), live idle timeout did not take effect", elapsed)
	}
}

func waitPoolWorkers(t *testing.T, engine *Engine, pool tasks.PoolID, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		stats, err := engine.Stats(ctx)
		cancel()
		if err == nil && stats.Pools[pool].Workers == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	stats, _ := engine.Stats(context.Background())
	t.Fatalf("pool %s workers=%d, want %d", pool, stats.Pools[pool].Workers, want)
}
