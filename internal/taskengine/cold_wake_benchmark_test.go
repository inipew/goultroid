package taskengine

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func waitBenchmarkPoolWorkers(b *testing.B, engine *Engine, pool tasks.PoolID, want int) {
	b.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		stats, err := engine.Stats(ctx)
		cancel()
		if err == nil {
			if got := stats.Pools[pool].Workers; got == want {
				return
			}
		}
		if time.Now().After(deadline) {
			b.Fatalf("pool %s workers did not reach %d", pool, want)
		}
		time.Sleep(100 * time.Microsecond)
	}
}

func benchmarkFirstTaskLatency(b *testing.B, zeroIdle bool) {
	pool := tasks.PoolID("wake")
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			pool: {
				Concurrency:    1,
				MinConcurrency: 1,
				ZeroIdle:       zeroIdle,
				IdleTimeout:    time.Millisecond,
				BacklogLimit:   16,
			},
		},
		ResultCapacity: 64,
	}
	if zeroIdle {
		cfg.Pools[pool] = PoolEngineConfig{
			Concurrency:    1,
			MinConcurrency: 0,
			ZeroIdle:       true,
			IdleTimeout:    time.Millisecond,
			BacklogLimit:   16,
		}
	}

	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer engine.Stop(context.Background())

	if zeroIdle {
		waitBenchmarkPoolWorkers(b, engine, pool, 0)
	} else {
		waitBenchmarkPoolWorkers(b, engine, pool, 1)
	}

	ctx := context.Background()
	handler := func(context.Context) error { return nil }
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := tasks.TaskID("wake-" + strconv.Itoa(i))
		ticket, err := engine.Submit(ctx, tasks.WorkSpec{
			ID:         id,
			QuotaOwner: "benchmark",
			Pool:       pool,
			Class:      tasks.PriorityInteractive,
			Handler:    handler,
		})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := ticket.Wait(ctx); err != nil {
			b.Fatal(err)
		}

		if zeroIdle {
			// Retirement wait is intentionally excluded: this benchmark measures
			// only the next cold admission -> completion latency after retirement.
			b.StopTimer()
			waitBenchmarkPoolWorkers(b, engine, pool, 0)
			b.StartTimer()
		}
	}
}

func BenchmarkTaskEngineFirstTaskAfterIdle(b *testing.B) {
	for _, tc := range []struct {
		name     string
		zeroIdle bool
	}{
		{name: "cold_zero_idle", zeroIdle: true},
		{name: "warm_min_one", zeroIdle: false},
	} {
		b.Run(tc.name, func(b *testing.B) {
			benchmarkFirstTaskLatency(b, tc.zeroIdle)
		})
	}
}
