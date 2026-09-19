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

func TestEngine_GuaranteedOnCompleteInvocation(t *testing.T) {
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

	// 1. Success case
	completeCh := make(chan tasks.TaskResult, 1)
	specSuccess := tasks.WorkSpec{
		ID:         "task-success",
		QuotaOwner: "owner-1",
		Pool:       "general",
		Handler:    func(ctx context.Context) error { return nil },
		OnComplete: func(res tasks.TaskResult) {
			completeCh <- res
		},
	}
	ticket, err := engine.Submit(context.Background(), specSuccess)
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	_, _ = ticket.Wait(context.Background())

	select {
	case res := <-completeCh:
		if res.Outcome != tasks.OutcomeCompleted {
			t.Errorf("expected OutcomeCompleted, got %v", res.Outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OnComplete callback on success")
	}

	// 2. Failure case
	failCh := make(chan tasks.TaskResult, 1)
	specFail := tasks.WorkSpec{
		ID:         "task-fail",
		QuotaOwner: "owner-1",
		Pool:       "general",
		Handler:    func(ctx context.Context) error { return errors.New("boom") },
		OnComplete: func(res tasks.TaskResult) {
			failCh <- res
		},
	}
	ticketFail, err := engine.Submit(context.Background(), specFail)
	if err != nil {
		t.Fatalf("submit fail error: %v", err)
	}
	_, _ = ticketFail.Wait(context.Background())

	select {
	case res := <-failCh:
		if res.Outcome != tasks.OutcomeFailed {
			t.Errorf("expected OutcomeFailed, got %v", res.Outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OnComplete callback on failure")
	}
}

func TestEngine_QueueDeadlineExpirySweep(t *testing.T) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 1, BacklogLimit: 10, PayloadBudget: 1000},
		},
		ResultCapacity: 10,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}
	defer engine.Stop(context.Background())

	blockerStarted := make(chan struct{})
	blockerRelease := make(chan struct{})

	specBlocker := tasks.WorkSpec{
		ID:         "blocker",
		QuotaOwner: "user-blocker",
		Pool:       "general",
		Handler: func(ctx context.Context) error {
			close(blockerStarted)
			<-blockerRelease
			return nil
		},
	}
	_, err := engine.Submit(context.Background(), specBlocker)
	if err != nil {
		t.Fatalf("submit blocker error: %v", err)
	}
	<-blockerStarted

	expiredOnComplete := make(chan tasks.TaskResult, 1)
	specExpiring := tasks.WorkSpec{
		ID:            "expiring-task",
		QuotaOwner:    "user-expiring",
		Pool:          "general",
		QueueDeadline: time.Now().UTC().Add(30 * time.Millisecond),
		Handler:       func(ctx context.Context) error { return nil },
		OnComplete: func(res tasks.TaskResult) {
			expiredOnComplete <- res
		},
	}

	ticket, err := engine.Submit(context.Background(), specExpiring)
	if err != nil {
		t.Fatalf("submit expiring error: %v", err)
	}

	// Wait for queue deadline to expire
	res, err := ticket.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait error: %v", err)
	}

	if res.Outcome != tasks.OutcomeTimedOut {
		t.Errorf("expected OutcomeTimedOut, got: %s", res.Outcome)
	}
	if res.Cause != tasks.CauseQueueExpired {
		t.Errorf("expected CauseQueueExpired, got: %s", res.Cause)
	}

	select {
	case callbackRes := <-expiredOnComplete:
		if callbackRes.Outcome != tasks.OutcomeTimedOut {
			t.Errorf("expected callback OutcomeTimedOut, got %v", callbackRes.Outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OnComplete callback on queue expiration")
	}

	close(blockerRelease)
}

func TestEngineMultiPoolSaturationAndFairness(t *testing.T) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"pool-fast": {Concurrency: 3, BacklogLimit: 5, PayloadBudget: 1000},
			"pool-slow": {Concurrency: 1, BacklogLimit: 2, PayloadBudget: 1000},
		},
		ResultCapacity: 50,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}
	defer engine.Stop(context.Background())

	var fastActive, slowActive atomic.Int32
	var fastMax, slowMax atomic.Int32

	blocker := make(chan struct{})
	fastTickets := make([]tasks.Ticket, 0, 8)
	slowTickets := make([]tasks.Ticket, 0, 3)

	// Fill pool-fast up to concurrency (3) with blocking tasks
	for i := 0; i < 3; i++ {
		idx := i
		spec := tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("fast-block-%d", idx)),
			QuotaOwner: "user-fast",
			Pool:       "pool-fast",
			Class:      tasks.PriorityNormal,
			Handler: func(ctx context.Context) error {
				curr := fastActive.Add(1)
				for {
					max := fastMax.Load()
					if curr <= max || fastMax.CompareAndSwap(max, curr) {
						break
					}
				}
				<-blocker
				fastActive.Add(-1)
				return nil
			},
		}
		ticket, err := engine.Submit(context.Background(), spec)
		if err != nil {
			t.Fatalf("fast submit %d error: %v", idx, err)
		}
		fastTickets = append(fastTickets, ticket)
	}

	// Fill pool-slow with 1 running task
	slowSpec := tasks.WorkSpec{
		ID:         "slow-block-0",
		QuotaOwner: "user-slow",
		Pool:       "pool-slow",
		Class:      tasks.PriorityNormal,
		Handler: func(ctx context.Context) error {
			curr := slowActive.Add(1)
			for {
				max := slowMax.Load()
				if curr <= max || slowMax.CompareAndSwap(max, curr) {
					break
				}
			}
			<-blocker
			slowActive.Add(-1)
			return nil
		},
	}
	st, err := engine.Submit(context.Background(), slowSpec)
	if err != nil {
		t.Fatalf("slow submit error: %v", err)
	}
	slowTickets = append(slowTickets, st)

	// Wait until active workers reach expected concurrency
	deadline := time.Now().Add(time.Second)
	for fastActive.Load() < 3 || slowActive.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("workers did not start in time: fast=%d, slow=%d", fastActive.Load(), slowActive.Load())
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Now queue backlog items in pool-slow (capacity = 2)
	for i := 1; i <= 2; i++ {
		idx := i
		spec := tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("slow-queue-%d", idx)),
			QuotaOwner: "user-slow",
			Pool:       "pool-slow",
			Class:      tasks.PriorityNormal,
			Handler: func(ctx context.Context) error {
				return nil
			},
		}
		ticket, err := engine.Submit(context.Background(), spec)
		if err != nil {
			t.Fatalf("slow queue submit %d error: %v", idx, err)
		}
		slowTickets = append(slowTickets, ticket)
	}

	// One more submission to pool-slow should exceed capacity and fail admission
	overflowSpec := tasks.WorkSpec{
		ID:         "slow-overflow",
		QuotaOwner: "user-slow",
		Pool:       "pool-slow",
		Class:      tasks.PriorityNormal,
		Handler: func(ctx context.Context) error {
			return nil
		},
	}
	_, err = engine.Submit(context.Background(), overflowSpec)
	if err == nil {
		t.Errorf("expected overflow submit to pool-slow to fail due to capacity")
	}

	// But pool-fast should still accept backlog items independently
	for i := 3; i < 6; i++ {
		idx := i
		spec := tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("fast-queue-%d", idx)),
			QuotaOwner: "user-fast",
			Pool:       "pool-fast",
			Class:      tasks.PriorityNormal,
			Handler: func(ctx context.Context) error {
				return nil
			},
		}
		ticket, err := engine.Submit(context.Background(), spec)
		if err != nil {
			t.Fatalf("fast queue submit %d error: %v", idx, err)
		}
		fastTickets = append(fastTickets, ticket)
	}

	// Unblock all running tasks
	close(blocker)

	// Verify all fast tickets complete successfully
	for _, ticket := range fastTickets {
		res, err := ticket.Wait(context.Background())
		if err != nil || !res.IsSuccess() {
			t.Errorf("fast ticket %v failed: res=%v, err=%v", ticket.TaskID(), res, err)
		}
	}

	// Verify all slow tickets complete successfully
	for _, ticket := range slowTickets {
		res, err := ticket.Wait(context.Background())
		if err != nil || !res.IsSuccess() {
			t.Errorf("slow ticket %v failed: res=%v, err=%v", ticket.TaskID(), res, err)
		}
	}

	// Concurrency should not exceed max
	if fastMax.Load() > 3 {
		t.Errorf("pool-fast concurrency exceeded: max observed %d > limit 3", fastMax.Load())
	}
	if slowMax.Load() > 1 {
		t.Errorf("pool-slow concurrency exceeded: max observed %d > limit 1", slowMax.Load())
	}
}

func TestNewDefaultConfigReturnsIndependentMaps(t *testing.T) {
	first := NewDefaultConfig()
	second := NewDefaultConfig()

	general := first.Pools["general"]
	general.Concurrency = 999
	first.Pools["general"] = general
	first.Pools["custom"] = PoolEngineConfig{Concurrency: 1}

	if got := second.Pools["general"].Concurrency; got != 8 {
		t.Fatalf("second default inherited mutation: concurrency=%d", got)
	}
	if _, exists := second.Pools["custom"]; exists {
		t.Fatal("default pool map is shared across calls")
	}
}

func TestNewEngineDoesNotReadMutableDefaultConfig(t *testing.T) {
	original := DefaultConfig
	defer func() { DefaultConfig = original }()

	DefaultConfig = Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"poisoned": {Concurrency: 1, BacklogLimit: 1, PayloadBudget: 1},
		},
		ResultCapacity: 1,
		InboxCapacity:  1,
	}

	engine := NewEngine(Config{})
	if _, exists := engine.config.Pools["poisoned"]; exists {
		t.Fatal("engine construction read mutable exported DefaultConfig")
	}
	if got := engine.config.Pools["general"].Concurrency; got != 8 {
		t.Fatalf("general concurrency=%d, want immutable default 8", got)
	}
	if engine.resultCapacity != 1000 {
		t.Fatalf("result capacity=%d, want immutable default 1000", engine.resultCapacity)
	}
}


type testRateLimitSignal struct {
	wait time.Duration
}

func (e *testRateLimitSignal) Error() string {
	return "rate limited"
}

func (e *testRateLimitSignal) Unwrap() error {
	return context.DeadlineExceeded
}

func (e *testRateLimitSignal) RateLimitWait() time.Duration {
	return e.wait
}

func TestEngine_PreservesRateLimitResultMetadata(t *testing.T) {
	cfg := Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 1, BacklogLimit: 10, PayloadBudget: 1000},
		},
		ResultCapacity: 10,
	}
	engine := NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}
	defer engine.Stop(context.Background())

	const wantWait = 17 * time.Second
	ticket, err := engine.Submit(context.Background(), tasks.WorkSpec{
		ID:         "rate-limited-task",
		QuotaOwner: "user-1",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler: func(context.Context) error {
			return fmt.Errorf("telegram rpc: %w", &testRateLimitSignal{wait: wantWait})
		},
	})
	if err != nil {
		t.Fatalf("submit rate-limited task: %v", err)
	}

	res, err := ticket.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait rate-limited task: %v", err)
	}
	if res.Outcome != tasks.OutcomeFailed {
		t.Fatalf("outcome=%s, want %s", res.Outcome, tasks.OutcomeFailed)
	}
	if res.Cause != tasks.CauseRateLimited {
		t.Fatalf("cause=%s, want %s", res.Cause, tasks.CauseRateLimited)
	}
	if res.RetryAfter != wantWait {
		t.Fatalf("retry_after=%s, want %s", res.RetryAfter, wantWait)
	}
	if res.Failure.Message == "" {
		t.Fatal("rate-limit diagnostic message must be preserved")
	}
}
