package taskengine

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestDefaultPoolsStartWithZeroPhysicalWorkers(t *testing.T) {
	e := NewEngine(NewDefaultConfig())
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())

	stats, err := e.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for pool, stat := range stats.Pools {
		if stat.Workers != 0 {
			t.Fatalf("default pool %s started %d physical workers, want zero-idle", pool, stat.Workers)
		}
	}
}

func TestZeroIdlePoolSpawnsOnDemandAndRetiresToZero(t *testing.T) {
	e := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"zero": {
				Concurrency: 2, MinConcurrency: 0, ZeroIdle: true,
				IdleTimeout: 10 * time.Millisecond, BacklogLimit: 8, PayloadBudget: 1 << 20,
			},
		},
		ResultCapacity: 8,
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())

	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID: "zero-idle-task", Pool: "zero", QuotaOwner: "owner",
		Handler: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ticket.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats, statErr := e.Stats(context.Background())
		if statErr != nil {
			t.Fatal(statErr)
		}
		if stats.Pools["zero"].Workers == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	stats, _ := e.Stats(context.Background())
	t.Fatalf("zero-idle pool retained %d worker(s)", stats.Pools["zero"].Workers)
}

func TestLegacyZeroMinStillMeansFixedSizeWithoutZeroIdle(t *testing.T) {
	e := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"legacy": {Concurrency: 2, MinConcurrency: 0, BacklogLimit: 8},
		},
		ResultCapacity: 8,
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())

	stats, err := e.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.Pools["legacy"].Workers; got != 2 {
		t.Fatalf("legacy zero MinConcurrency workers=%d, want fixed size 2", got)
	}
}
