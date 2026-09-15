package taskengine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestEngine_SubmitAndExecute(t *testing.T) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 2, BacklogLimit: 10, PayloadBudget: 1000},
		},
		ResultCapacity: 10,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}
	defer engine.Stop(context.Background())

	var executed atomic.Bool
	spec := tasks.WorkSpec{
		ID:         "task-1",
		QuotaOwner: "user-1",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler: func(ctx context.Context) error {
			executed.Store(true)
			return nil
		},
	}

	ticket, err := engine.Submit(context.Background(), spec)
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}

	res, err := ticket.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait error: %v", err)
	}

	if !res.IsSuccess() {
		t.Errorf("expected IsSuccess() == true, got: %s", res.Outcome)
	}
	if !executed.Load() {
		t.Errorf("handler was not executed")
	}

	snap, ok := engine.Snapshot("task-1")
	if !ok || snap.State != tasks.StateCompleted {
		t.Errorf("snapshot state mismatch: %v (found: %v)", snap.State, ok)
	}
}

func TestEngine_CancelQueuedTask(t *testing.T) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 1, BacklogLimit: 10, PayloadBudget: 1000},
		},
		ResultCapacity: 10,
	}
	engine := NewEngine(cfg)
	_ = engine.Start(context.Background())
	defer engine.Stop(context.Background())

	blockerStarted := make(chan struct{})
	blockerRelease := make(chan struct{})

	// Submit blocker task to occupy the only slot
	blocker := tasks.WorkSpec{
		ID:         "blocker",
		QuotaOwner: "user-1",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler: func(ctx context.Context) error {
			close(blockerStarted)
			<-blockerRelease
			return nil
		},
	}
	_, err := engine.Submit(context.Background(), blocker)
	if err != nil {
		t.Fatalf("failed to submit blocker: %v", err)
	}
	<-blockerStarted

	// Submit second task which must be queued
	queuedSpec := tasks.WorkSpec{
		ID:         "queued-task",
		QuotaOwner: "user-2",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler: func(ctx context.Context) error {
			return nil
		},
	}
	ticket, err := engine.Submit(context.Background(), queuedSpec)
	if err != nil {
		t.Fatalf("failed to submit queued task: %v", err)
	}

	// Cancel queued task before blocker releases
	receipt, err := engine.Cancel("queued-task", tasks.CauseUserCancel)
	if err != nil || !receipt.Accepted || receipt.State != tasks.StateCancelled {
		t.Fatalf("cancel receipt mismatch: accepted=%v state=%s err=%v", receipt.Accepted, receipt.State, err)
	}

	res, _ := ticket.Wait(context.Background())
	if res.Outcome != tasks.OutcomeCancelled || res.Cause != tasks.CauseUserCancel {
		t.Errorf("expected outcome cancelled, got: %s (cause: %s)", res.Outcome, res.Cause)
	}

	close(blockerRelease)
}

func TestEngine_ResultBackpressure(t *testing.T) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 1, BacklogLimit: 10, PayloadBudget: 1000},
		},
		ResultCapacity: 1, // Only 1 result credit available
	}
	engine := NewEngine(cfg)
	_ = engine.Start(context.Background())
	defer engine.Stop(context.Background())

	spec1 := tasks.WorkSpec{
		ID:         "t-1",
		QuotaOwner: "user-1",
		Pool:       "general",
		Handler: func(ctx context.Context) error {
			time.Sleep(50 * time.Millisecond)
			return nil
		},
	}
	_, err := engine.Submit(context.Background(), spec1)
	if err != nil {
		t.Fatalf("expected spec1 to be accepted, got: %v", err)
	}

	// Second task should be rejected immediately because ResultCapacity is 1
	spec2 := tasks.WorkSpec{
		ID:         "t-2",
		QuotaOwner: "user-2",
		Pool:       "general",
		Handler:    func(ctx context.Context) error { return nil },
	}
	_, err = engine.Submit(context.Background(), spec2)
	if err == nil {
		t.Fatalf("expected spec2 to be rejected due to result backpressure")
	}

	var admErr *tasks.AdmissionError
	if !errors.As(err, &admErr) || admErr.Reason != tasks.ReasonResultBackpressure {
		t.Errorf("expected ReasonResultBackpressure, got: %v", err)
	}
}

func TestEngine_QuiesceAndDrain(t *testing.T) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 2, BacklogLimit: 10, PayloadBudget: 1000},
		},
		ResultCapacity: 10,
	}
	engine := NewEngine(cfg)
	_ = engine.Start(context.Background())

	spec := tasks.WorkSpec{
		ID:         "task-1",
		QuotaOwner: "user-1",
		Pool:       "general",
		Handler: func(ctx context.Context) error {
			time.Sleep(20 * time.Millisecond)
			return nil
		},
	}
	ticket, err := engine.Submit(context.Background(), spec)
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}

	// Quiesce engine
	if err := engine.Quiesce(context.Background()); err != nil {
		t.Fatalf("quiesce error: %v", err)
	}

	// New submission after Quiesce should be rejected
	spec2 := tasks.WorkSpec{
		ID:         "task-2",
		QuotaOwner: "user-1",
		Pool:       "general",
		Handler:    func(ctx context.Context) error { return nil },
	}
	if _, err := engine.Submit(context.Background(), spec2); err == nil {
		t.Fatalf("expected submit after quiesce to be rejected")
	}

	// Drain should wait for task-1 to complete
	drainCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := engine.Drain(drainCtx); err != nil {
		t.Fatalf("drain error: %v", err)
	}

	res, _ := ticket.Wait(context.Background())
	if !res.IsSuccess() {
		t.Errorf("expected drained task to complete successfully")
	}
}

func TestEngine_CancelScope(t *testing.T) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 1, BacklogLimit: 10, PayloadBudget: 1000},
		},
		ResultCapacity: 10,
	}
	engine := NewEngine(cfg)
	_ = engine.Start(context.Background())
	defer engine.Stop(context.Background())

	engine.SetOwnerLimits("plugin:media", admission.OwnerLimits{MaxWaiting: 10, MaxActive: 5})

	blockerStarted := make(chan struct{})
	blockerRelease := make(chan struct{})

	// Blocker
	specBlocker := tasks.WorkSpec{
		ID:         "b-1",
		QuotaOwner: "system",
		Pool:       "general",
		Handler: func(ctx context.Context) error {
			close(blockerStarted)
			<-blockerRelease
			return nil
		},
	}
	_, _ = engine.Submit(context.Background(), specBlocker)
	<-blockerStarted

	// Submit 3 tasks for plugin:media generation 1
	specMedia1 := tasks.WorkSpec{
		ID:         "m-1",
		Scope:      tasks.ScopeIdentity{Owner: "plugin:media", Generation: 1},
		QuotaOwner: "plugin:media",
		Pool:       "general",
		Handler:    func(ctx context.Context) error { return nil },
	}
	specMedia2 := tasks.WorkSpec{
		ID:         "m-2",
		Scope:      tasks.ScopeIdentity{Owner: "plugin:media", Generation: 1},
		QuotaOwner: "plugin:media",
		Pool:       "general",
		Handler:    func(ctx context.Context) error { return nil },
	}
	// Submit 1 task for plugin:other
	specOther := tasks.WorkSpec{
		ID:         "o-1",
		Scope:      tasks.ScopeIdentity{Owner: "plugin:other", Generation: 1},
		QuotaOwner: "plugin:other",
		Pool:       "general",
		Handler:    func(ctx context.Context) error { return nil },
	}

	_, _ = engine.Submit(context.Background(), specMedia1)
	_, _ = engine.Submit(context.Background(), specMedia2)
	_, _ = engine.Submit(context.Background(), specOther)

	// Cancel scope plugin:media generation 1
	cancelled := engine.CancelScope(tasks.ScopeIdentity{Owner: "plugin:media", Generation: 1}, tasks.CauseScopeClosed)
	if cancelled != 2 {
		t.Fatalf("expected 2 cancelled tasks for plugin:media, got %d", cancelled)
	}

	// Verify plugin:other is still queued
	snap, ok := engine.Snapshot("o-1")
	if !ok || snap.State != tasks.StateQueued {
		t.Errorf("expected plugin:other task to still be queued, got: %v", snap.State)
	}

	close(blockerRelease)
}
