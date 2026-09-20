package taskengine

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"github.com/inipew/goultroid/internal/tasks"
)

// BenchmarkB0_IdleOverhead measures background CPU and timer wake overhead when idle.
func BenchmarkB0_IdleOverhead(b *testing.B) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"idle": {Concurrency: 4, BacklogLimit: 1000},
		},
		ResultCapacity: 1000,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer engine.Stop(context.Background())

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// Verify engine state snapshot latency during idle
		_, ok := engine.Snapshot("non-existent-task")
		if ok {
			b.Fatal("unexpected task found")
		}
	}
}

// BenchmarkB1_TinyEphemeralTask measures offered load, admission ops/s, end-to-end latency, and alloc/op.
func BenchmarkB1_TinyEphemeralTask(b *testing.B) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"ephemeral": {Concurrency: 8, BacklogLimit: 100000},
		},
		ResultCapacity: 100000,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer engine.Stop(context.Background())

	ctx := context.Background()
	noOpHandler := func(ctx context.Context) error { return nil }

	b.ResetTimer()
	b.ReportAllocs()

	var idCounter atomic.Uint64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			taskID := tasks.TaskID("task-b1-" + strconv.FormatUint(idCounter.Add(1), 10))
			spec := tasks.WorkSpec{
				ID:         taskID,
				QuotaOwner: "user-b1",
				Pool:       "ephemeral",
				Class:      tasks.PriorityNormal,
				Handler:    noOpHandler,
			}
			ticket, err := engine.Submit(ctx, spec)
			if err != nil {
				b.Fatalf("Submit error: %v", err)
			}
			_, err = ticket.Wait(ctx)
			if err != nil {
				b.Fatalf("Wait error: %v", err)
			}
		}
	})
}

// BenchmarkB2_IOCommandMixFairness measures multi-user command dispatch across multiple quota owners.
func BenchmarkB2_IOCommandMixFairness(b *testing.B) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"interactive": {Concurrency: 8, BacklogLimit: 100000},
		},
		ResultCapacity: 100000,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer engine.Stop(context.Background())

	ctx := context.Background()
	owners := []tasks.OwnerID{"user:1", "user:2", "user:3", "user:4"}

	b.ResetTimer()
	b.ReportAllocs()

	var seq atomic.Uint64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := seq.Add(1)
			owner := owners[n%uint64(len(owners))]
			spec := tasks.WorkSpec{
				ID:         tasks.TaskID(fmt.Sprintf("cmd-%s-%d", owner, n)),
				QuotaOwner: owner,
				Pool:       "interactive",
				Class:      tasks.PriorityNormal,
				Handler: func(ctx context.Context) error {
					time.Sleep(10 * time.Microsecond)
					return nil
				},
			}
			ticket, err := engine.Submit(ctx, spec)
			if err != nil {
				b.Fatalf("Submit error: %v", err)
			}
			_, _ = ticket.Wait(ctx)
		}
	})
}

// BenchmarkB3_CPUAndInteractiveIsolation measures latency of interactive tasks when CPU pool is saturated.
func BenchmarkB3_CPUAndInteractiveIsolation(b *testing.B) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"cpu_heavy":   {Concurrency: 4, BacklogLimit: 1000},
			"interactive": {Concurrency: 4, BacklogLimit: 10000},
		},
		ResultCapacity: 15000,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer engine.Stop(context.Background())

	ctx := context.Background()
	stopCPU := make(chan struct{})
	var wg sync.WaitGroup

	// Saturate cpu_heavy pool
	for i := 0; i < 4; i++ {
		wg.Add(1)
		spec := tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("cpu-heavy-%d", i)),
			QuotaOwner: "sys-cpu",
			Pool:       "cpu_heavy",
			Class:      tasks.PriorityBackground,
			Handler: func(ctx context.Context) error {
				defer wg.Done()
				<-stopCPU
				return nil
			},
		}
		if _, err := engine.Submit(ctx, spec); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		spec := tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("interactive-%d", i)),
			QuotaOwner: "user-ui",
			Pool:       "interactive",
			Class:      tasks.PriorityInteractive,
			Handler: func(ctx context.Context) error {
				return nil
			},
		}
		ticket, err := engine.Submit(ctx, spec)
		if err != nil {
			b.Fatalf("Submit interactive error: %v", err)
		}
		res, err := ticket.Wait(ctx)
		if err != nil || !res.IsSuccess() {
			b.Fatalf("Interactive task failed: res=%v err=%v", res, err)
		}
	}

	b.StopTimer()
	close(stopCPU)
	wg.Wait()
}

// BenchmarkB4_PeriodicDueBurst measures burst dispatch of 500 timer tasks.
func BenchmarkB4_PeriodicDueBurst(b *testing.B) {
	const burstTasks = 500
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"scheduler": {Concurrency: 16, BacklogLimit: 50000},
		},
		ResultCapacity: 50000,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer engine.Stop(context.Background())

	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		tickets := make([]tasks.Ticket, 0, burstTasks)
		for j := 0; j < burstTasks; j++ {
			owner := tasks.OwnerID(fmt.Sprintf("scheduler:periodic:%d", j%25))
			spec := tasks.WorkSpec{
				ID:         tasks.TaskID(fmt.Sprintf("sched-%d-%d", i, j)),
				QuotaOwner: owner,
				Pool:       "scheduler",
				Class:      tasks.PriorityMaintenance,
				Handler:    func(ctx context.Context) error { return nil },
			}
			ticket, err := engine.Submit(ctx, spec)
			if err != nil {
				b.Fatalf("Submit error: %v", err)
			}
			tickets = append(tickets, ticket)
		}
		for _, ticket := range tickets {
			if _, err := ticket.Wait(ctx); err != nil {
				b.Fatalf("Wait error: %v", err)
			}
		}
	}
	b.ReportMetric(float64(burstTasks), "tasks/burst")
}

// BenchmarkB5_DurableStoreThroughput measures SQLite attempt lease prepare and result commit latency.
func BenchmarkB5_DurableStoreThroughput(b *testing.B) {
	db, err := database.Open(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := jobsqlite.InitSchema(ctx, db.DB); err != nil {
		b.Fatal(err)
	}

	store := jobsqlite.NewStore(db.DB)
	// Seed job definition and occurrence
	_, err = db.DB.ExecContext(ctx, `
		INSERT INTO job_definitions (id, scope_owner, quota_owner, handler_type, updated_at)
		VALUES ('bench-job', 'bench', 'user:bench', 'bench.handler', CURRENT_TIMESTAMP)
	`)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		occID := fmt.Sprintf("occ-%d", i)
		_, err := db.DB.ExecContext(ctx, `
			INSERT INTO job_occurrences (id, job_id, scheduled_for, occurrence_key, ready_at, updated_at)
			VALUES (?, 'bench-job', CURRENT_TIMESTAMP, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		`, occID, occID)
		if err != nil {
			b.Fatal(err)
		}

		attempt, err := store.PrepareAttemptLease(ctx, occID, fmt.Sprintf("task-%d", i), time.Minute)
		if err != nil {
			b.Fatalf("PrepareAttemptLease error: %v", err)
		}

		err = store.CommitAttemptResult(ctx, attempt.ID, attempt.LeaseEpoch, "completed", []byte(`{"status":"ok"}`), "")
		if err != nil {
			b.Fatalf("CommitAttemptResult error: %v", err)
		}
	}
}

// BenchmarkB6_CancellationStorm measures cancel-to-terminal latency on queued and running tasks.
func BenchmarkB6_CancellationStorm(b *testing.B) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"cancels": {Concurrency: 2, BacklogLimit: 10000},
		},
		ResultCapacity: 10000,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer engine.Stop(context.Background())

	ctx := context.Background()
	blocker := make(chan struct{})

	// Pre-block concurrency
	for i := 0; i < 2; i++ {
		spec := tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("blocker-%d", i)),
			QuotaOwner: "blocker",
			Pool:       "cancels",
			Class:      tasks.PriorityNormal,
			Handler: func(ctx context.Context) error {
				<-blocker
				return nil
			},
		}
		if _, err := engine.Submit(ctx, spec); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		taskID := tasks.TaskID(fmt.Sprintf("cancel-task-%d", i))
		spec := tasks.WorkSpec{
			ID:         taskID,
			QuotaOwner: "user-cancel",
			Pool:       "cancels",
			Class:      tasks.PriorityNormal,
			Handler:    func(ctx context.Context) error { return nil },
		}
		ticket, err := engine.Submit(ctx, spec)
		if err != nil {
			b.Fatalf("Submit error: %v", err)
		}

		// Immediate cancellation
		_, err = engine.Cancel(taskID, tasks.CauseUserCancel)
		if err != nil {
			b.Fatalf("Cancel error: %v", err)
		}
		_, _ = ticket.Wait(ctx)
	}

	b.StopTimer()
	close(blocker)
}

// BenchmarkB7_MemoryRetentionAndHeapPlateau tests that repeated bursts with bounded retention
// produce a stable heap plateau without monotonic memory growth.
func BenchmarkB7_MemoryRetentionAndHeapPlateau(b *testing.B) {
	const maxRetained = 500
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"retention": {Concurrency: 8, BacklogLimit: 5000},
		},
		ResultCapacity:      5000,
		MaxTerminalRetained: maxRetained,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer engine.Stop(context.Background())

	ctx := context.Background()
	noOp := func(ctx context.Context) error { return nil }

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Run a burst of 100 tasks
		for j := 0; j < 100; j++ {
			taskID := tasks.TaskID(fmt.Sprintf("retention-%d-%d", i, j))
			spec := tasks.WorkSpec{
				ID:         taskID,
				QuotaOwner: "user-ret",
				Pool:       "retention",
				Class:      tasks.PriorityNormal,
				Handler:    noOp,
			}
			ticket, err := engine.Submit(ctx, spec)
			if err != nil {
				b.Fatalf("Submit error: %v", err)
			}
			_, _ = ticket.Wait(ctx)
		}
	}

	// Verify terminal memory is strictly bounded via control-loop stats
	// (terminalOrder is runLoop-owned; mu does not protect it).
	runtime.GC()
	statsCtx, statsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer statsCancel()
	rep, err := engine.sendControl(statsCtx, engineRequest{op: opStats, reply: make(chan engineReply, 1)})
	if err != nil {
		b.Fatalf("stats error: %v", err)
	}
	count := rep.stats.terminalCount

	if count > maxRetained {
		b.Errorf("terminal order count %d exceeded MaxTerminalRetained %d", count, maxRetained)
	}
}

// BenchmarkB8_ShutdownDrainLatency measures drain latency of in-flight work during Quiesce.
func BenchmarkB8_ShutdownDrainLatency(b *testing.B) {
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		cfg := Config{
			Pools: map[tasks.PoolID]PoolEngineConfig{
				"drain": {Concurrency: 4, BacklogLimit: 100},
			},
			ResultCapacity: 100,
		}
		engine := NewEngine(cfg)
		_ = engine.Start(context.Background())

		// Submit 4 quick tasks
		for j := 0; j < 4; j++ {
			spec := tasks.WorkSpec{
				ID:         tasks.TaskID(fmt.Sprintf("drain-%d-%d", i, j)),
				QuotaOwner: "user-drain",
				Pool:       "drain",
				Class:      tasks.PriorityNormal,
				Handler: func(ctx context.Context) error {
					time.Sleep(50 * time.Microsecond)
					return nil
				},
			}
			_, _ = engine.Submit(context.Background(), spec)
		}

		b.StartTimer()
		_ = engine.Quiesce(context.Background())
		_ = engine.Stop(context.Background())
	}
}
