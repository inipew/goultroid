package taskengine

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func startTestEngine(t *testing.T, cfg Config) *Engine {
	t.Helper()
	e := NewEngine(cfg)
	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(func() {
		_ = e.Stop(context.Background())
	})
	return e
}

func waitForState(t *testing.T, e *Engine, id tasks.TaskID, want tasks.TaskState, timeout time.Duration) tasks.TaskSnapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snap, ok := e.Snapshot(id)
		if ok && snap.State == want {
			return snap
		}
		time.Sleep(2 * time.Millisecond)
	}
	snap, _ := e.Snapshot(id)
	t.Fatalf("timed out waiting for %s=%s (got %+v)", id, want, snap)
	return tasks.TaskSnapshot{}
}

// A3: Dispatching -> Started -> Running. StartedAt must be zero until Running
// and must come from the worker boundary, never from dispatch time.
func TestDispatchingStartedRunningOrder(t *testing.T) {
	e := startTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 1, BacklogLimit: 10, PayloadBudget: 1 << 20}},
		ResultCapacity:      10,
		MaxTerminalRetained: 10,
		DecisionTimeout:     5 * time.Second,
		InboxCapacity:       64,
	})
	release := make(chan struct{})
	_, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "task-dispatch-order",
		QuotaOwner: "owner-a",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { <-release; return nil },
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	snap := waitForState(t, e, "task-dispatch-order", tasks.StateRunning, 5*time.Second)
	close(release)
	if snap.StartedAt.IsZero() {
		t.Fatalf("Running task must carry worker StartedAt")
	}
	if snap.StartedAt.Before(snap.QueuedAt) {
		t.Fatalf("StartedAt %v before QueuedAt %v", snap.StartedAt, snap.QueuedAt)
	}
}

// A3 invariant: pre-running snapshots never carry StartedAt.
func TestPreRunningHasNoStartedAt(t *testing.T) {
	e := startTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 1, BacklogLimit: 10, PayloadBudget: 1 << 20}},
		ResultCapacity:      10,
		MaxTerminalRetained: 10,
		DecisionTimeout:     5 * time.Second,
		InboxCapacity:       64,
	})
	block := make(chan struct{})
	_, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "task-blocker",
		QuotaOwner: "owner-a",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { <-block; return nil },
	})
	if err != nil {
		t.Fatalf("submit blocker: %v", err)
	}
	waitForState(t, e, "task-blocker", tasks.StateRunning, 5*time.Second)
	_, err = e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "task-queued",
		QuotaOwner: "owner-a",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("submit queued: %v", err)
	}
	// The second task cannot run while the single slot is held. Whatever
	// pre-running state it reports, StartedAt must be zero.
	deadline := time.Now().Add(200 * time.Millisecond)
	sawPreRunning := false
	for time.Now().Before(deadline) {
		snap, ok := e.Snapshot("task-queued")
		if !ok {
			t.Fatalf("missing snapshot")
		}
		if snap.State == tasks.StateQueued || snap.State == tasks.StateDispatching {
			sawPreRunning = true
			if !snap.StartedAt.IsZero() {
				t.Fatalf("pre-running state %s must not carry StartedAt", snap.State)
			}
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !sawPreRunning {
		t.Logf("task finished before pre-running snapshot could be observed; invariant vacuously holds")
	}
	close(block)
}

// A2: cancelled context never blocks on the inbox; decision is bounded.
func TestSubmitCancelledContextDoesNotBlock(t *testing.T) {
	e := startTestEngine(t, DefaultConfig)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := e.Submit(ctx, tasks.WorkSpec{
		ID:         "task-cancelled-ctx",
		QuotaOwner: "owner-a",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { return nil },
	})
	if err == nil {
		t.Fatalf("expected error for cancelled context")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("cancelled submit blocked too long: %v", time.Since(start))
	}
}

// A4: CancelScope installs a generation barrier; later admissions are rejected.
func TestCancelScopeBarrierRejectsLaterSubmit(t *testing.T) {
	e := startTestEngine(t, DefaultConfig)
	scope := tasks.ScopeIdentity{Owner: "plugin:x", Generation: 7}
	if n := e.CancelScope(scope, tasks.CauseUserCancel); n != 0 {
		t.Fatalf("expected 0 cancelled, got %d", n)
	}
	_, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "task-scoped",
		Scope:      scope,
		QuotaOwner: "plugin:x",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { return nil },
	})
	if err == nil {
		t.Fatalf("expected scope-closed rejection")
	}
}

// A4: stale/forged Started events cannot promote a task.
func TestWorkerStartedFencingIgnoresStalePermit(t *testing.T) {
	e := startTestEngine(t, Config{
		Pools:               map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 1, BacklogLimit: 10, PayloadBudget: 1 << 20}},
		ResultCapacity:      10,
		MaxTerminalRetained: 10,
		DecisionTimeout:     5 * time.Second,
		InboxCapacity:       64,
	})
	release := make(chan struct{})
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "task-fence",
		QuotaOwner: "owner-a",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
		Handler:    func(ctx context.Context) error { <-release; return nil },
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	_ = ticket
	// Forged permit for an unknown task must be ignored (no panic, no state change).
	e.sendInternal(engineRequest{op: opWorkerStarted, taskID: "task-unknown", permit: newPermit("general", 0, 1, "task-unknown", 999, nil), started: time.Now().UTC()})
	time.Sleep(50 * time.Millisecond)
	close(release)
	snap := waitForState(t, e, "task-fence", tasks.StateCompleted, 5*time.Second)
	_ = snap
}
