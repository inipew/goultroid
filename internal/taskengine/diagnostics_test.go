package taskengine

import (
	"context"
	"errors"
	"sync"
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
	runningRec := &taskRecord{spec: tasks.WorkSpec{ID: "running", Pool: "diag"}}
	engine.setTaskState(runningRec, tasks.StateRunning)
	engine.registry["running"] = runningRec
	dispatchingRec := &taskRecord{spec: tasks.WorkSpec{ID: "dispatching", Pool: "diag"}}
	engine.setTaskState(dispatchingRec, tasks.StateDispatching)
	engine.registry["dispatching"] = dispatchingRec
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

func TestPoolRuntimeCountersMatchRegistryAcrossTransitions(t *testing.T) {
	engine := NewEngine(Config{Pools: map[tasks.PoolID]PoolEngineConfig{
		"p1": {Concurrency: 4, BacklogLimit: 10},
		"p2": {Concurrency: 2, BacklogLimit: 10},
	}, ResultCapacity: 10})

	assertMatchesRegistry := func(step string) {
		t.Helper()
		wantRunning := make(map[tasks.PoolID]int)
		wantDispatching := make(map[tasks.PoolID]int)
		for _, rec := range engine.registry {
			switch rec.state {
			case tasks.StateRunning:
				wantRunning[rec.spec.Pool]++
			case tasks.StateDispatching:
				wantDispatching[rec.spec.Pool]++
			}
		}
		got := engine.snapshotPoolRuntimeStats()
		for _, pool := range []tasks.PoolID{"p1", "p2"} {
			if got[pool].Running != wantRunning[pool] || got[pool].Dispatching != wantDispatching[pool] {
				t.Fatalf("%s pool=%s: got running=%d dispatching=%d, want running=%d dispatching=%d",
					step, pool, got[pool].Running, got[pool].Dispatching, wantRunning[pool], wantDispatching[pool])
			}
		}
	}

	r1 := &taskRecord{spec: tasks.WorkSpec{ID: "t1", Pool: "p1"}}
	r2 := &taskRecord{spec: tasks.WorkSpec{ID: "t2", Pool: "p1"}}
	r3 := &taskRecord{spec: tasks.WorkSpec{ID: "t3", Pool: "p2"}}
	engine.registry["t1"], engine.registry["t2"], engine.registry["t3"] = r1, r2, r3

	engine.setTaskState(r1, tasks.StateQueued)
	engine.setTaskState(r2, tasks.StateDispatching)
	engine.setTaskState(r3, tasks.StateDispatching)
	assertMatchesRegistry("initial_dispatching")

	// Idempotent transition must not double increment.
	engine.setTaskState(r2, tasks.StateDispatching)
	assertMatchesRegistry("idempotent_dispatching")

	engine.setTaskState(r2, tasks.StateRunning)
	engine.setTaskState(r3, tasks.StateCommitPending)
	assertMatchesRegistry("running_and_commit_pending")

	// Idempotent running transition must not double increment.
	engine.setTaskState(r2, tasks.StateRunning)
	assertMatchesRegistry("idempotent_running")

	engine.setTaskState(r2, tasks.StateCompleted)
	engine.setTaskState(r3, tasks.StateRecoveryRequired)
	engine.setTaskState(r1, tasks.StateCancelled)
	assertMatchesRegistry("all_terminal")
}

func TestStats_LaneWorkers(t *testing.T) {
	engine := NewEngine(Config{Pools: map[tasks.PoolID]PoolEngineConfig{
		"diag": {Concurrency: 1, BacklogLimit: 2},
	}, ResultCapacity: 4, DeliveryConcurrency: 1})
	if err := engine.SetDurabilityConcurrency(1); err != nil {
		t.Fatal(err)
	}
	engine.delivery.idleTimeout = 10 * time.Millisecond
	engine.durability.idleTimeout = 10 * time.Millisecond
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = engine.Stop(ctx)
	})
	started := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		if !engine.delivery.reserve() || !engine.delivery.enqueueReserved(func(tasks.TaskResult) {
			started <- struct{}{}
			<-release
		}, tasks.TaskResult{}) {
			t.Fatal("enqueue delivery")
		}
		if !engine.durability.enqueue(func() {
			started <- struct{}{}
			<-release
		}) {
			t.Fatal("enqueue durability")
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("lane worker did not start")
		}
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, err := engine.Stats(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.DeliveryLane.Workers == 1 && got.DeliveryLane.Pending == 1 && got.DeliveryLane.Active == 1 && got.DeliveryLane.WorkerLimit == 1 &&
			got.DurabilityLane.Workers == 1 && got.DurabilityLane.Pending == 1 && got.DurabilityLane.Active == 1 && got.DurabilityLane.WorkerLimit == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	got, err := engine.Stats(context.Background())
	if err != nil || got.DeliveryLane != (LaneRuntimeStats{WorkerLimit: 1, Workers: 1, Pending: 1, Active: 1}) ||
		got.DurabilityLane != (LaneRuntimeStats{WorkerLimit: 1, Workers: 1, Pending: 1, Active: 1}) {
		t.Fatalf("lane snapshot = %+v, error = %v", got, err)
	}
	releaseOnce.Do(func() { close(release) })
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, err = engine.Stats(context.Background())
		if err == nil && got.DeliveryLane.Workers == 0 && got.DeliveryLane.Pending == 0 && got.DeliveryLane.Active == 0 &&
			got.DurabilityLane.Workers == 0 && got.DurabilityLane.Pending == 0 && got.DurabilityLane.Active == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("lane workers did not retire: %+v", got)
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

func TestRuntimeStatsExposeLifecycleState(t *testing.T) {
	engine := NewEngine(DefaultConfig)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })

	stats, err := engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Accepting || stats.Quiesced {
		t.Fatalf("running lifecycle stats = %+v, want accepting and not quiesced", stats)
	}

	if err := engine.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats, err = engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Accepting || !stats.Quiesced {
		t.Fatalf("quiesced lifecycle stats = %+v, want not accepting and quiesced", stats)
	}
}

func TestStats_AdmissionRejectionsCounter(t *testing.T) {
	engine := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 1, BacklogLimit: 1, PayloadBudget: 1000},
		},
		ResultCapacity: 1,
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Stop(context.Background()) }()

	// 1. Initial rejections must be 0
	stats, err := engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.AdmissionRejections != 0 {
		t.Fatalf("initial AdmissionRejections = %d, want 0", stats.AdmissionRejections)
	}

	blockCh := make(chan struct{})
	defer close(blockCh)

	// 2. Submit task 1 - succeeds and occupies ResultCapacity slot
	spec1 := tasks.WorkSpec{
		ID:         "t-rej-1",
		QuotaOwner: "u1",
		Pool:       "general",
		Handler: func(ctx context.Context) error {
			<-blockCh
			return nil
		},
	}
	_, err = engine.Submit(context.Background(), spec1)
	if err != nil {
		t.Fatalf("submit spec1 failed: %v", err)
	}

	// 3. Submit task 2 - rejected due to ResultCapacity full
	spec2 := tasks.WorkSpec{
		ID:         "t-rej-2",
		QuotaOwner: "u2",
		Pool:       "general",
		Handler:    func(ctx context.Context) error { return nil },
	}
	_, err = engine.Submit(context.Background(), spec2)
	if err == nil {
		t.Fatal("expected submit spec2 to be rejected")
	}

	// 4. Verify AdmissionRejections incremented to 1
	stats, err = engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.AdmissionRejections != 1 {
		t.Fatalf("AdmissionRejections = %d, want 1", stats.AdmissionRejections)
	}

	// 5. Submit task 3 - also rejected
	spec3 := tasks.WorkSpec{
		ID:         "t-rej-3",
		QuotaOwner: "u3",
		Pool:       "general",
		Handler:    func(ctx context.Context) error { return nil },
	}
	_, err = engine.Submit(context.Background(), spec3)
	if err == nil {
		t.Fatal("expected submit spec3 to be rejected")
	}

	// 6. Verify AdmissionRejections incremented to 2
	stats, err = engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.AdmissionRejections != 2 {
		t.Fatalf("AdmissionRejections = %d, want 2", stats.AdmissionRejections)
	}
}

func TestStats_AdmissionRejections_ExhaustivePaths(t *testing.T) {
	engine := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 2, BacklogLimit: 10},
		},
		ResultCapacity: 10,
	})

	// 1. Submit on unstarted engine -> rejected with ReasonEngineQuiescing
	specUnstarted := tasks.WorkSpec{
		ID:         "t-unstarted",
		QuotaOwner: "u1",
		Pool:       "general",
		Handler:    func(ctx context.Context) error { return nil },
	}
	if _, err := engine.Submit(context.Background(), specUnstarted); err == nil {
		t.Fatal("expected submit to fail on unstarted engine")
	}

	// 2. Start engine and verify initial count from step 1 is preserved
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Stop(context.Background()) }()

	stats, err := engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.AdmissionRejections != 1 {
		t.Fatalf("after unstarted submit, AdmissionRejections = %d, want 1", stats.AdmissionRejections)
	}

	// 3. WorkSpec validation error: empty ID
	specInvalidID := tasks.WorkSpec{
		ID:         "",
		QuotaOwner: "u1",
		Pool:       "general",
		Handler:    func(ctx context.Context) error { return nil },
	}
	if _, err := engine.Submit(context.Background(), specInvalidID); err == nil {
		t.Fatal("expected submit to fail with empty ID")
	}
	stats, err = engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.AdmissionRejections != 2 {
		t.Fatalf("after invalid ID, AdmissionRejections = %d, want 2", stats.AdmissionRejections)
	}

	// 4. Unknown pool error
	specInvalidPool := tasks.WorkSpec{
		ID:         "t-unknown-pool",
		QuotaOwner: "u1",
		Pool:       "non-existent-pool",
		Handler:    func(ctx context.Context) error { return nil },
	}
	if _, err := engine.Submit(context.Background(), specInvalidPool); err == nil {
		t.Fatal("expected submit to fail with unknown pool")
	}
	stats, err = engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.AdmissionRejections != 3 {
		t.Fatalf("after unknown pool, AdmissionRejections = %d, want 3", stats.AdmissionRejections)
	}

	// 5. Pre-cancelled caller context (verify no double counting)
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	specCancelled := tasks.WorkSpec{
		ID:         "t-cancelled",
		QuotaOwner: "u1",
		Pool:       "general",
		Handler:    func(ctx context.Context) error { return nil },
	}
	if _, err := engine.Submit(cancelledCtx, specCancelled); err == nil {
		t.Fatal("expected submit to fail with cancelled context")
	}
	stats, err = engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.AdmissionRejections != 4 {
		t.Fatalf("after cancelled context, AdmissionRejections = %d, want 4", stats.AdmissionRejections)
	}

	// 6. Successful submission must NOT increment AdmissionRejections
	taskDone := make(chan struct{})
	specValid := tasks.WorkSpec{
		ID:         "t-valid",
		QuotaOwner: "u1",
		Pool:       "general",
		Handler: func(ctx context.Context) error {
			close(taskDone)
			return nil
		},
	}
	if _, err := engine.Submit(context.Background(), specValid); err != nil {
		t.Fatalf("unexpected submit failure: %v", err)
	}
	stats, err = engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.AdmissionRejections != 4 {
		t.Fatalf("successful submit must not increment AdmissionRejections, got %d, want 4", stats.AdmissionRejections)
	}

	// Wait for task to finish
	select {
	case <-taskDone:
	case <-time.After(2 * time.Second):
		t.Fatal("task did not complete in time")
	}

	// 7. Duplicate Task ID rejection
	if _, err := engine.Submit(context.Background(), specValid); err == nil {
		t.Fatal("expected duplicate task ID submit to fail")
	}
	stats, err = engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.AdmissionRejections != 5 {
		t.Fatalf("after duplicate ID, AdmissionRejections = %d, want 5", stats.AdmissionRejections)
	}

	// 8. Execution failure must NOT increment AdmissionRejections
	taskFailDone := make(chan struct{})
	specFailing := tasks.WorkSpec{
		ID:         "t-failing-exec",
		QuotaOwner: "u1",
		Pool:       "general",
		Handler: func(ctx context.Context) error {
			defer close(taskFailDone)
			return errors.New("handler execution failure")
		},
	}
	if _, err := engine.Submit(context.Background(), specFailing); err != nil {
		t.Fatalf("submit for failing handler should succeed, got: %v", err)
	}
	select {
	case <-taskFailDone:
	case <-time.After(2 * time.Second):
		t.Fatal("failing task did not run in time")
	}
	stats, err = engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.AdmissionRejections != 5 {
		t.Fatalf("execution failure must not increment AdmissionRejections, got %d, want 5", stats.AdmissionRejections)
	}
}
