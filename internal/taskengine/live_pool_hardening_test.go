package taskengine

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestLivePoolShrinkConvergesBeforeReplacementDispatch(t *testing.T) {
	engine := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"adaptive": {Concurrency: 8, MinConcurrency: 8, IdleTimeout: time.Minute, BacklogLimit: 32, PayloadBudget: 1 << 20},
		},
		ResultCapacity: 32,
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })

	firstStarted := make(chan struct{}, 8)
	firstRelease := make(chan struct{})
	tickets := make([]tasks.Ticket, 0, 16)
	for i := 0; i < 8; i++ {
		ticket, err := engine.Submit(context.Background(), tasks.WorkSpec{
			ID: tasks.TaskID(fmt.Sprintf("shrink-first-%d", i)), QuotaOwner: "owner", Pool: "adaptive",
			Handler: func(context.Context) error {
				firstStarted <- struct{}{}
				<-firstRelease
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		tickets = append(tickets, ticket)
	}
	for i := 0; i < 8; i++ {
		select {
		case <-firstStarted:
		case <-time.After(time.Second):
			t.Fatal("initial 8-worker wave did not start")
		}
	}

	secondStarted := make(chan struct{}, 8)
	secondRelease := make(chan struct{})
	var secondActive atomic.Int32
	var secondMax atomic.Int32
	for i := 0; i < 8; i++ {
		ticket, err := engine.Submit(context.Background(), tasks.WorkSpec{
			ID: tasks.TaskID(fmt.Sprintf("shrink-second-%d", i)), QuotaOwner: "owner", Pool: "adaptive",
			Handler: func(context.Context) error {
				active := secondActive.Add(1)
				for {
					previous := secondMax.Load()
					if active <= previous || secondMax.CompareAndSwap(previous, active) {
						break
					}
				}
				secondStarted <- struct{}{}
				<-secondRelease
				secondActive.Add(-1)
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		tickets = append(tickets, ticket)
	}

	if err := engine.ConfigurePool(context.Background(), "adaptive", PoolEngineConfig{
		Concurrency: 2, MinConcurrency: 2, IdleTimeout: time.Minute, BacklogLimit: 32, PayloadBudget: 1 << 20,
	}); err != nil {
		t.Fatal(err)
	}
	close(firstRelease)

	for i := 0; i < 2; i++ {
		select {
		case <-secondStarted:
		case <-time.After(time.Second):
			t.Fatal("replacement work did not converge to the shrunken pool")
		}
	}
	time.Sleep(50 * time.Millisecond)
	if got := secondMax.Load(); got > 2 {
		t.Fatalf("replacement concurrency exceeded shrunken maximum: got %d, want <=2", got)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats, err := engine.Stats(context.Background())
		if err == nil {
			pool := stats.Pools["adaptive"]
			if pool.Workers == 2 && pool.MaxWorkers == 2 && pool.Running == 2 && pool.Waiting == 6 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	stats, err := engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pool := stats.Pools["adaptive"]
	if pool.Workers != 2 || pool.MaxWorkers != 2 || pool.Running != 2 || pool.Waiting != 6 {
		t.Fatalf("pool did not converge after shrink: %+v", pool)
	}

	close(secondRelease)
	for _, ticket := range tickets {
		if _, err := ticket.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := secondMax.Load(); got > 2 {
		t.Fatalf("post-shrink task concurrency exceeded 2: %d", got)
	}
}

func TestLivePoolZeroMinimumPreservesFixedSizeSemantics(t *testing.T) {
	engine := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"fixed": {Concurrency: 3, MinConcurrency: 0, BacklogLimit: 8, PayloadBudget: 1024},
		},
		ResultCapacity: 8,
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })

	waitPoolWorkers(t, engine, "fixed", 3, time.Second)
	if err := engine.ConfigurePool(context.Background(), "fixed", PoolEngineConfig{
		Concurrency: 2, MinConcurrency: 0, BacklogLimit: 8, PayloadBudget: 1024,
	}); err != nil {
		t.Fatal(err)
	}
	waitPoolWorkers(t, engine, "fixed", 2, time.Second)

	stats, err := engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pool := stats.Pools["fixed"]
	if pool.MinWorkers != 2 || pool.MaxWorkers != 2 {
		t.Fatalf("live fixed-size normalization mismatch: %+v", pool)
	}
}

func TestStartupPoolValidationUsesEffectiveBounds(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{
			name: "minimum exceeds default concurrency",
			cfg: Config{Pools: map[tasks.PoolID]PoolEngineConfig{
				"p": {Concurrency: 0, MinConcurrency: 5},
			}},
		},
		{
			name: "negative terminal retention",
			cfg:  Config{MaxTerminalRetained: -1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateConfig(tc.cfg); err == nil {
				t.Fatal("ValidateConfig accepted invalid startup bounds")
			}
		})
	}

	valid := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"p": {Concurrency: 0, MinConcurrency: 4, BacklogLimit: 4, PayloadBudget: 1024},
		},
		ResultCapacity: 4,
	}
	if err := ValidateConfig(valid); err != nil {
		t.Fatalf("effective default concurrency should allow minimum 4: %v", err)
	}
	engine := NewEngine(valid)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })
	waitPoolWorkers(t, engine, "p", 4, time.Second)
}

func TestLivePoolRejectsNegativeAdmissionLimits(t *testing.T) {
	engine := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"adaptive": {Concurrency: 2, MinConcurrency: 1, IdleTimeout: time.Second, BacklogLimit: 4, PayloadBudget: 1024},
		},
		ResultCapacity: 8,
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })

	cases := []PoolEngineConfig{
		{Concurrency: 2, MinConcurrency: 1, IdleTimeout: -time.Second, BacklogLimit: 4, PayloadBudget: 1024},
		{Concurrency: 2, MinConcurrency: 1, IdleTimeout: time.Second, BacklogLimit: -1, PayloadBudget: 1024},
		{Concurrency: 2, MinConcurrency: 1, IdleTimeout: time.Second, BacklogLimit: 4, PayloadBudget: -1},
	}
	for _, cfg := range cases {
		if err := engine.ConfigurePool(context.Background(), "adaptive", cfg); err == nil {
			t.Fatalf("ConfigurePool accepted invalid live limits: %+v", cfg)
		}
	}

	if err := engine.ConfigurePool(context.Background(), "adaptive", PoolEngineConfig{
		Concurrency: 2, MinConcurrency: 1, IdleTimeout: time.Second, BacklogLimit: 0, PayloadBudget: 0,
	}); err != nil {
		t.Fatalf("zero live admission limits must preserve startup semantics: %v", err)
	}
}
