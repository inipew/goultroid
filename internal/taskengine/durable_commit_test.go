package taskengine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

// Controllable CommitPump stub (Phase C6). Modes:
//
//	auto: run the op immediately, returning opErr (nil for success).
//	manual: hold ops until completeAll runs them synchronously.
//	reject: Enqueue fails, exercising the bounded direct fallback.
type stubPumpMode int

const (
	stubAuto stubPumpMode = iota
	stubManual
	stubReject
)

type stubOp struct {
	op    func(ctx context.Context) error
	resCh chan error
}

type stubPump struct {
	mu       sync.Mutex
	mode     stubPumpMode
	opErr    error
	pending  []stubOp
	enqueued int
}

func (p *stubPump) Enqueue(ctx context.Context, op func(ctx context.Context) error) (<-chan error, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode == stubReject {
		return nil, errors.New("stub pump saturated")
	}
	p.enqueued++
	resCh := make(chan error, 1)
	if p.mode == stubManual {
		p.pending = append(p.pending, stubOp{op: op, resCh: resCh})
		return resCh, nil
	}
	opErr := p.opErr
	go func() {
		if opErr != nil {
			resCh <- opErr
			return
		}
		resCh <- op(ctx)
	}()
	return resCh, nil
}

func (p *stubPump) completeAll() {
	p.mu.Lock()
	pending := p.pending
	p.pending = nil
	p.mu.Unlock()
	for _, op := range pending {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		op.resCh <- op.op(ctx)
		cancel()
	}
}

func durableTestEngine(t *testing.T, cfg Config, pump CommitPump) *Engine {
	t.Helper()
	e := NewEngine(cfg)
	e.SetCommitPump(pump)
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	return e
}

func waitForEngineState(t *testing.T, e *Engine, id tasks.TaskID, want tasks.TaskState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if snap, ok := e.Snapshot(id); ok && snap.State == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	snap, _ := e.Snapshot(id)
	t.Fatalf("timed out waiting for %s=%s (got %+v)", id, want, snap)
}

func commitRecorder(calls *atomic.Int32) tasks.CommitFunc {
	return func(ctx context.Context, res tasks.TaskResult) error {
		calls.Add(1)
		return nil
	}
}

// C1/C2: physical completion moves a durability-required task to
// CommitPending with the result credit held; the ticket resolves only after
// the pump acknowledgement commits it.
func TestDurableCommitHoldsCreditUntilAck(t *testing.T) {
	pump := &stubPump{mode: stubManual}
	e := durableTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 2, BacklogLimit: 10, PayloadBudget: 1 << 20}},
		ResultCapacity:      10,
		MaxTerminalRetained: 10,
		DecisionTimeout:     5 * time.Second,
	}, pump)
	var calls atomic.Int32
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "durable-1",
		QuotaOwner: "owner",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Job:        &tasks.OccurrenceRef{JobID: "j", OccurrenceID: "occ", AttemptID: "att", LeaseEpoch: 1},
		Handler:    func(ctx context.Context) error { return nil },
		Commit:     commitRecorder(&calls),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForEngineState(t, e, "durable-1", tasks.StateCommitPending, 5*time.Second)

	select {
	case <-ticket.Done():
		t.Fatalf("ticket resolved before durable acknowledgement")
	default:
	}
	stats := engineStatsOf(t, e)
	if stats.resultSlotsHeld != 1 {
		t.Fatalf("result credit must be held in CommitPending, held=%d", stats.resultSlotsHeld)
	}
	if stats.commitPending != 1 {
		t.Fatalf("commitPending=%d, want 1", stats.commitPending)
	}

	pump.completeAll()
	res, err := ticket.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !res.IsSuccess() {
		t.Fatalf("expected success after ack, got %+v", res)
	}
	if calls.Load() != 1 {
		t.Fatalf("commit invoked %d times, want exactly once", calls.Load())
	}
	snap, _ := e.Snapshot("durable-1")
	if snap.State != tasks.StateCompleted {
		t.Fatalf("state=%s, want completed", snap.State)
	}
	stats = engineStatsOf(t, e)
	if stats.resultSlotsHeld != 0 || stats.commitPending != 0 {
		t.Fatalf("credits not released after commit: %+v", stats)
	}
}

// Recommendation 2: tasks without Commit resolve at physical completion even
// when a pump is configured, and never touch the pump.
func TestNonDurableSkipsCommitPending(t *testing.T) {
	pump := &stubPump{mode: stubManual}
	e := durableTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 2, BacklogLimit: 10, PayloadBudget: 1 << 20}},
		ResultCapacity:      10,
		MaxTerminalRetained: 10,
		DecisionTimeout:     5 * time.Second,
	}, pump)
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "plain-1",
		QuotaOwner: "owner",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	res, err := ticket.Wait(context.Background())
	if err != nil || !res.IsSuccess() {
		t.Fatalf("wait: %+v %v", res, err)
	}
	pump.mu.Lock()
	enqueued := pump.enqueued
	pump.mu.Unlock()
	if enqueued != 0 {
		t.Fatalf("non-durable task must not touch the pump, enqueued=%d", enqueued)
	}
}

// C4: commit failure resolves to RecoveryRequired with the physical outcome
// preserved and the persistence cause attached; credits are released.
func TestCommitFailureYieldsRecoveryRequired(t *testing.T) {
	pump := &stubPump{mode: stubAuto, opErr: errors.New("sqlite unavailable")}
	e := durableTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 2, BacklogLimit: 10, PayloadBudget: 1 << 20}},
		ResultCapacity:      10,
		MaxTerminalRetained: 10,
		DecisionTimeout:     5 * time.Second,
	}, pump)
	var calls atomic.Int32
	var hookRes tasks.TaskResult
	hookDone := make(chan struct{})
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "durable-fail",
		QuotaOwner: "owner",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { return nil },
		Commit:     commitRecorder(&calls),
		OnComplete: func(r tasks.TaskResult) {
			hookRes = r
			close(hookDone)
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForEngineState(t, e, "durable-fail", tasks.StateRecoveryRequired, 5*time.Second)
	res, err := ticket.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if res.Outcome != tasks.OutcomeCompleted {
		t.Fatalf("physical outcome must be preserved, got %s", res.Outcome)
	}
	if res.Cause != tasks.CausePersistenceFailure {
		t.Fatalf("cause=%s, want persistence_failure", res.Cause)
	}
	select {
	case <-hookDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("post-commit hook not delivered")
	}
	if hookRes.Cause != tasks.CausePersistenceFailure {
		t.Fatalf("hook cause=%s", hookRes.Cause)
	}
	stats := engineStatsOf(t, e)
	if stats.resultSlotsHeld != 0 || stats.commitPending != 0 {
		t.Fatalf("credits not released after recovery: %+v", stats)
	}
}

// C4: stale and unknown acknowledgements are fenced off.
func TestCommitAckFencingIgnoresStale(t *testing.T) {
	pump := &stubPump{mode: stubManual}
	e := durableTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 2, BacklogLimit: 10, PayloadBudget: 1 << 20}},
		ResultCapacity:      10,
		MaxTerminalRetained: 10,
		DecisionTimeout:     5 * time.Second,
	}, pump)
	var calls atomic.Int32
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "durable-fence",
		QuotaOwner: "owner",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { return nil },
		Commit:     commitRecorder(&calls),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForEngineState(t, e, "durable-fence", tasks.StateCommitPending, 5*time.Second)
	pump.completeAll()
	if _, err := ticket.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	// Late duplicate ack for the same task and an ack for an unknown task
	// must both be ignored without state corruption.
	e.sendInternal(engineRequest{op: opCommitAck, taskID: "durable-fence", commitSeq: 1, ackErr: errors.New("late")})
	e.sendInternal(engineRequest{op: opCommitAck, taskID: "no-such-task", commitSeq: 1})
	time.Sleep(50 * time.Millisecond)
	snap, _ := e.Snapshot("durable-fence")
	if snap.State != tasks.StateCompleted {
		t.Fatalf("stale ack corrupted state: %s", snap.State)
	}
	if calls.Load() != 1 {
		t.Fatalf("commit invoked %d times", calls.Load())
	}
}

// C5: forced shutdown abandons CommitPending as RecoveryRequired so tickets
// resolve instead of hanging; the store stays the source of truth.
func TestShutdownAbandonsPendingToRecovery(t *testing.T) {
	pump := &stubPump{mode: stubManual}
	e := NewEngine(Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 2, BacklogLimit: 10, PayloadBudget: 1 << 20}},
		ResultCapacity:      10,
		MaxTerminalRetained: 10,
		DecisionTimeout:     5 * time.Second,
	})
	e.SetCommitPump(pump)
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "durable-stop",
		QuotaOwner: "owner",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { return nil },
		Commit:     commitRecorder(&calls),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForEngineState(t, e, "durable-stop", tasks.StateCommitPending, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = e.Stop(ctx) // Drain expires; StopFinalize must abandon the pending commit.
	res, err := ticket.Wait(context.Background())
	if err != nil {
		t.Fatalf("ticket must resolve after stop, got err %v", err)
	}
	if res.Cause != tasks.CausePersistenceFailure {
		t.Fatalf("cause=%s, want persistence_failure", res.Cause)
	}
	// Snapshot is unavailable after Stop (control loop gone); the ticket's
	// done-close edge carries the authoritative terminal result.
	res2, ok := ticket.Result()
	if !ok || res2.Cause != tasks.CausePersistenceFailure {
		t.Fatalf("ticket result after stop: %+v %v", res2, ok)
	}
}

// C3: a saturated or missing pump falls back to the bounded direct commit;
// durability is still attempted exactly once.
func TestPumpSaturationFallsBackToDirectCommit(t *testing.T) {
	pump := &stubPump{mode: stubReject}
	e := durableTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 2, BacklogLimit: 10, PayloadBudget: 1 << 20}},
		ResultCapacity:      10,
		MaxTerminalRetained: 10,
		DecisionTimeout:     5 * time.Second,
	}, pump)
	var calls atomic.Int32
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "durable-fb",
		QuotaOwner: "owner",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { return nil },
		Commit:     commitRecorder(&calls),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	res, err := ticket.Wait(context.Background())
	if err != nil || !res.IsSuccess() {
		t.Fatalf("fallback commit must still resolve success: %+v %v", res, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("commit invoked %d times", calls.Load())
	}
}

// C1/C5: held commit credits exert backpressure instead of unbounded growth.
func TestCommitPendingExertsBackpressure(t *testing.T) {
	pump := &stubPump{mode: stubManual}
	e := durableTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 2, BacklogLimit: 10, PayloadBudget: 1 << 20}},
		ResultCapacity:      2,
		MaxTerminalRetained: 10,
		DecisionTimeout:     5 * time.Second,
	}, pump)
	var calls atomic.Int32
	for i := 0; i < 2; i++ {
		_, err := e.Submit(context.Background(), tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("held-%d", i)),
			QuotaOwner: "owner",
			Pool:       "general",
			Class:      tasks.PriorityNormal,
			Handler:    func(ctx context.Context) error { return nil },
			Commit:     commitRecorder(&calls),
		})
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	waitForEngineState(t, e, "held-0", tasks.StateCommitPending, 5*time.Second)
	waitForEngineState(t, e, "held-1", tasks.StateCommitPending, 5*time.Second)
	_, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "held-2",
		QuotaOwner: "owner",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { return nil },
		Commit:     commitRecorder(&calls),
	})
	if err == nil {
		t.Fatalf("expected backpressure rejection while commit credits are held")
	}
	if !errors.Is(err, tasks.ErrResultBackpressure) {
		t.Fatalf("expected result backpressure, got %v", err)
	}
	pump.completeAll()
	waitForEngineState(t, e, "held-0", tasks.StateCompleted, 5*time.Second)
	third, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "held-2",
		QuotaOwner: "owner",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { return nil },
		Commit:     commitRecorder(&calls),
	})
	if err != nil {
		t.Fatalf("submit after commit drain: %v", err)
	}
	waitForEngineState(t, e, "held-2", tasks.StateCommitPending, 5*time.Second)
	pump.completeAll()
	if _, err := third.Wait(context.Background()); err != nil {
		t.Fatalf("wait held-2: %v", err)
	}
	waitForEngineState(t, e, "held-2", tasks.StateCompleted, 5*time.Second)
}
