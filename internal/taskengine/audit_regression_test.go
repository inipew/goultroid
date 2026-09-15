package taskengine

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/tasks"
)

func auditEngine(t *testing.T) *Engine {
	t.Helper()
	e := NewEngine(Config{Pools: map[tasks.PoolID]PoolEngineConfig{"a": {Concurrency: 1, BacklogLimit: 10}, "b": {Concurrency: 1, BacklogLimit: 10}}, ResultCapacity: 20})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = e.Stop(ctx)
	})
	return e
}

func waitAuditTicket(t *testing.T, ticket tasks.Ticket) tasks.TaskResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := ticket.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestEngineCompletionUnblocksGlobalQuota(t *testing.T) {
	for _, pool := range []tasks.PoolID{"a", "b"} {
		t.Run(string(pool), func(t *testing.T) {
			e := auditEngine(t)
			e.SetOwnerLimits("owner", admission.OwnerLimits{MaxActive: 1, MaxWaiting: 10})
			release := make(chan struct{})
			var once atomic.Bool
			defer func() {
				if once.CompareAndSwap(false, true) {
					close(release)
				}
			}()
			first, err := e.Submit(context.Background(), tasks.WorkSpec{ID: "first", Pool: "a", QuotaOwner: "owner", Handler: func(context.Context) error { <-release; return nil }})
			if err != nil {
				t.Fatal(err)
			}
			second, err := e.Submit(context.Background(), tasks.WorkSpec{ID: "second", Pool: pool, QuotaOwner: "owner", Handler: func(context.Context) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			if second.State() != tasks.StateQueued {
				t.Fatal("owner quota did not queue second task")
			}
			once.Store(true)
			close(release)
			waitAuditTicket(t, first)
			if res := waitAuditTicket(t, second); !res.IsSuccess() {
				t.Fatal(res)
			}
		})
	}
}

func TestEngineRejectsDuplicateAndInvalidAdmission(t *testing.T) {
	e := auditEngine(t)
	spec := tasks.WorkSpec{ID: "id", Pool: "a", QuotaOwner: "owner", Handler: func(context.Context) error { return nil }}
	ticket, err := e.Submit(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	waitAuditTicket(t, ticket)
	if _, err := e.Submit(context.Background(), spec); err == nil {
		t.Fatal("duplicate task accepted")
	}
	spec.ID = "expired"
	spec.QueueDeadline = time.Now().Add(-time.Second)
	if _, err := e.Submit(context.Background(), spec); !errors.Is(err, tasks.ErrDeadlineExpired) {
		t.Fatal(err)
	}
	if _, found := e.Snapshot(spec.ID); found {
		t.Fatal("rejected task registered")
	}
	spec.ID = "unresolved"
	spec.QueueDeadline = time.Time{}
	spec.Handler = nil
	spec.HandlerRef = "unknown"
	if _, err := e.Submit(context.Background(), spec); !errors.Is(err, tasks.ErrUnknownHandler) {
		t.Fatal(err)
	}
}

func TestEngineCompletionRunsOnceOutsideWorker(t *testing.T) {
	e := auditEngine(t)
	callbackStarted, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	spec := tasks.WorkSpec{ID: "callback", Pool: "a", QuotaOwner: "owner", Handler: func(context.Context) error { return nil }, OnComplete: func(tasks.TaskResult) {
		if calls.Add(1) == 1 {
			close(callbackStarted)
		}
		<-release
		panic("isolated callback")
	}}
	ticket, err := e.Submit(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	waitAuditTicket(t, ticket) // A blocked callback must not block the physical result.
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("missing callback")
	}
	// Drain covers bounded delivery (Phase B5): release the blocking callback
	// first, then drain. The panic must be isolated and delivered exactly once.
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("completion called %d times", calls.Load())
	}
}

func TestEngineStopFinalizesQueuedBehindUncooperativeHandler(t *testing.T) {
	e := auditEngine(t)
	release := make(chan struct{})
	defer close(release)
	_, err := e.Submit(context.Background(), tasks.WorkSpec{ID: "blocked", Pool: "a", QuotaOwner: "owner", Handler: func(context.Context) error { <-release; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{ID: "queued", Pool: "a", QuotaOwner: "other", Handler: func(context.Context) error { t.Error("queued handler executed after stop"); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if res := waitAuditTicket(t, ticket); res.Outcome != tasks.OutcomeCancelled || res.Cause != tasks.CauseShutdown {
		t.Fatal(res)
	}
}

func TestEngineCopiesAdmittedPayloadAndOccurrence(t *testing.T) {
	e := auditEngine(t)
	payload := []byte("original")
	ref := &tasks.OccurrenceRef{AttemptID: "original"}
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{ID: "copy", Pool: "a", QuotaOwner: "owner", Input: payload, Job: ref, Handler: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	payload[0] = 'X'
	ref.AttemptID = "changed"
	result := waitAuditTicket(t, ticket)
	if result.AttemptID != "original" {
		t.Fatal("caller changed admitted attempt identity")
	}
	// The ticket's record pointer is stable from admission; after Wait the
	// done-close edge makes the immutable spec bytes safe to read without
	// touching the runLoop-owned registry map.
	et, ok := ticket.(*engineTicket)
	if !ok {
		t.Fatal("expected engine ticket")
	}
	if string(et.rec.spec.Input.([]byte)) != "original" {
		t.Fatal("caller changed admitted payload")
	}
}

func TestEngineCancelScopeBarrierBlocksSubsequentSubmit(t *testing.T) {
	e := auditEngine(t)
	scope := tasks.ScopeIdentity{Owner: "plugin:weather", Generation: 1}

	// 1. Submit initial task under scope
	ticket1, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "task-pre",
		Scope:      scope,
		QuotaOwner: "plugin:weather",
		Pool:       "a",
		Handler:    func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("unexpected submit error: %v", err)
	}
	_ = waitAuditTicket(t, ticket1)

	// 2. Cancel scope
	_ = e.CancelScope(scope, tasks.CauseScopeClosed)

	// 3. Submit after CancelScope must be immediately rejected at admission barrier
	_, err = e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "task-post",
		Scope:      scope,
		QuotaOwner: "plugin:weather",
		Pool:       "a",
		Handler:    func(context.Context) error { return nil },
	})
	if !errors.Is(err, tasks.ErrScopeClosed) {
		t.Fatalf("expected ErrScopeClosed for late submit on cancelled scope, got: %v", err)
	}
}

func TestEngineLateCancellationAuditing(t *testing.T) {
	e := auditEngine(t)
	taskStarted := make(chan struct{})
	allowFinish := make(chan struct{})

	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "task-running-cancel",
		QuotaOwner: "owner-late",
		Pool:       "a",
		Handler: func(ctx context.Context) error {
			close(taskStarted)
			<-allowFinish
			// Return nil (success), simulating handler that finished without checking ctx
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	<-taskStarted

	// Cancel while running
	receipt, err := e.Cancel("task-running-cancel", tasks.CauseUserCancel)
	if err != nil || !receipt.Accepted || receipt.State != tasks.StateRunning {
		t.Fatalf("expected running cancel accepted, got receipt=%+v, err=%v", receipt, err)
	}

	// Allow handler to finish (simulating late finish)
	close(allowFinish)

	res := waitAuditTicket(t, ticket)
	if res.Outcome != tasks.OutcomeCancelled || res.Cause != tasks.CauseUserCancel {
		t.Fatalf("expected late cancellation to preserve OutcomeCancelled and CauseUserCancel, got: %+v", res)
	}
}

func TestEngineConfigValidationAndDefensiveCopy(t *testing.T) {
	// 1. Validation of negative parameters
	invalidCfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"bad": {Concurrency: -1},
		},
	}
	if err := ValidateConfig(invalidCfg); err == nil {
		t.Fatal("expected error for negative concurrency")
	}

	invalidCap := Config{
		ResultCapacity: -5,
	}
	if err := ValidateConfig(invalidCap); err == nil {
		t.Fatal("expected error for negative result capacity")
	}

	// 2. Defensive copy of pools
	poolsMap := map[tasks.PoolID]PoolEngineConfig{
		"pool1": {Concurrency: 5, BacklogLimit: 20},
	}
	cfg := Config{
		Pools:          poolsMap,
		ResultCapacity: 50,
	}
	eng := NewEngine(cfg)

	// Mutate external map
	poolsMap["pool1"] = PoolEngineConfig{Concurrency: 999}
	delete(poolsMap, "pool1")

	// Verify engine's internal config was not mutated
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if eng.config.Pools["pool1"].Concurrency != 5 {
		t.Fatalf("engine pool config was mutated by caller: got %d, expected 5", eng.config.Pools["pool1"].Concurrency)
	}
}

func TestEngineTerminalRecordEviction(t *testing.T) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"test": {Concurrency: 2, BacklogLimit: 20},
		},
		MaxTerminalRetained: 2,
	}
	e := NewEngine(cfg)
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())

	// Submit 4 tasks that finish immediately
	for i := 1; i <= 4; i++ {
		taskID := tasks.TaskID(fmt.Sprintf("evict-task-%d", i))
		ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
			ID:         taskID,
			Pool:       "test",
			QuotaOwner: "test-owner",
			Handler:    func(ctx context.Context) error { return nil },
		})
		if err != nil {
			t.Fatalf("failed to submit task %d: %v", i, err)
		}
		res, err := ticket.Wait(context.Background())
		if err != nil || !res.IsSuccess() {
			t.Fatalf("task %d failed: res=%+v err=%v", i, res, err)
		}
	}

	// Only at most 2 terminal tasks should be retained in the registry.
	// Registry is runLoop-owned (single writer); assert through the public
	// Snapshot API instead of poking internals.
	retained := 0
	for i := 1; i <= 4; i++ {
		if _, ok := e.Snapshot(tasks.TaskID(fmt.Sprintf("evict-task-%d", i))); ok {
			retained++
		}
	}
	if retained > 2 {
		t.Fatalf("expected retained snapshots <= 2, got %d", retained)
	}
	// The oldest tasks (1 and 2) should have been evicted
	if _, exists := e.Snapshot("evict-task-1"); exists {
		t.Fatalf("expected evict-task-1 to be evicted")
	}
	if _, exists := e.Snapshot("evict-task-2"); exists {
		t.Fatalf("expected evict-task-2 to be evicted")
	}
	// The newest tasks (3 and 4) should still be in the registry
	if _, exists := e.Snapshot("evict-task-3"); !exists {
		t.Fatalf("expected evict-task-3 to be present")
	}
	if _, exists := e.Snapshot("evict-task-4"); !exists {
		t.Fatalf("expected evict-task-4 to be present")
	}
}

func TestEngineEventDrivenDeadlineSweeper(t *testing.T) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"p": {Concurrency: 1, BacklogLimit: 10},
		},
	}
	e := NewEngine(cfg)
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())

	blockerStarted := make(chan struct{})
	blockerRelease := make(chan struct{})
	defer close(blockerRelease)

	// Block worker 0
	_, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "blocker",
		Pool:       "p",
		QuotaOwner: "test-owner",
		Handler: func(ctx context.Context) error {
			close(blockerStarted)
			<-blockerRelease
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-blockerStarted

	// Submit queued task with a very short queue deadline (50ms)
	expiredTicket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:            "queued-deadline-task",
		Pool:          "p",
		QuotaOwner:    "test-owner",
		QueueDeadline: time.Now().UTC().Add(50 * time.Millisecond),
		Handler: func(ctx context.Context) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait should observe timeout caused by the dynamic sweepLoop wakeup
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := expiredTicket.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	if res.Outcome != tasks.OutcomeTimedOut || res.Cause != tasks.CauseQueueExpired {
		t.Fatalf("expected OutcomeTimedOut with CauseQueueExpired, got %+v", res)
	}
}

func TestEngineDecisionTimeoutAndLinearizationCancel(t *testing.T) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"p": {Concurrency: 1, BacklogLimit: 10},
		},
		DecisionTimeout: 50 * time.Millisecond,
	}
	e := NewEngine(cfg)
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())

	// 1. Submit with already-cancelled context
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.Submit(cancelledCtx, tasks.WorkSpec{
		ID:         "cancelled-early",
		Pool:       "p",
		QuotaOwner: "owner",
		Handler:    func(ctx context.Context) error { return nil },
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}

	// 2. Normal submit within decision timeout succeeds
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "valid-decision",
		Pool:       "p",
		QuotaOwner: "owner",
		Handler:    func(ctx context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("unexpected error on valid submit: %v", err)
	}
	res, err := ticket.Wait(context.Background())
	if err != nil || !res.IsSuccess() {
		t.Fatalf("expected success, got res=%+v err=%v", res, err)
	}
}
