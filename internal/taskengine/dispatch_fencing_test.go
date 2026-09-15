package taskengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type heldAssignment struct {
	assignment tasks.WorkerAssignment
	events     tasks.WorkerEvents
}

type holdingWorkers struct {
	slot     tasks.WorkerSlot
	assigned chan heldAssignment
}

func (w *holdingWorkers) Slots() []tasks.WorkerSlot { return []tasks.WorkerSlot{w.slot} }
func (w *holdingWorkers) Assign(_ context.Context, assignment tasks.WorkerAssignment, events tasks.WorkerEvents) error {
	select {
	case w.assigned <- heldAssignment{assignment: assignment, events: events}:
		return nil
	default:
		return errors.New("holding worker already owns an assignment")
	}
}

func newHoldingEngine(t *testing.T) (*Engine, *holdingWorkers, tasks.HandlerRef, tasks.ScopeIdentity) {
	t.Helper()
	cfg := engineConfig()
	cfg.Pools = map[tasks.PoolID]PoolLimits{
		"general": {Workers: 1, MaxWaiting: 8, MaxWaitingBytes: 1 << 20},
	}
	cfg.DefaultOwner.MaxActive = 1
	catalog, handler := testCatalog(t, cfg)
	workers := &holdingWorkers{
		slot:     tasks.WorkerSlot{Pool: "general", WorkerID: "general-1", Generation: 1},
		assigned: make(chan heldAssignment, 1),
	}
	engine, err := New(cfg, catalog, workers)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = engine.Stop(ctx)
	})
	scope, err := tasks.NewScopeIdentity("scope:dispatch-fence", 1)
	if err != nil {
		t.Fatal(err)
	}
	return engine, workers, handler, scope
}

func holdingSpec(t *testing.T, handler tasks.HandlerRef, scope tasks.ScopeIdentity, id tasks.TaskID) tasks.WorkSpec {
	t.Helper()
	payload, err := tasks.NewPayloadRef("test", 1, []byte(id))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := tasks.NewWorkSpec(tasks.WorkSpecParams{
		ID: id, Scope: scope, QuotaOwner: "owner", Pool: "general",
		Class: tasks.PriorityNormal, Cause: tasks.CauseManual, Handler: handler, Input: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestEngine_CancelWinsBeforeStartAndPreventsStartGrant(t *testing.T) {
	engine, workers, handler, scope := newHoldingEngine(t)
	if _, err := engine.Submit(context.Background(), holdingSpec(t, handler, scope, "cancel-before-start")); err != nil {
		t.Fatal(err)
	}

	var held heldAssignment
	select {
	case held = <-workers.assigned:
	case <-time.After(time.Second):
		t.Fatal("task was not physically assigned")
	}
	if receipt, err := engine.Cancel("cancel-before-start", tasks.CancelCaller); err != nil || !receipt.Requested {
		t.Fatalf("Cancel() receipt=%+v err=%v", receipt, err)
	}
	if err := held.events.Started(held.assignment.Permit(), time.Now().UTC()); !errors.Is(err, ErrWorkerEventInvalid) {
		t.Fatalf("Started() error=%v want ErrWorkerEventInvalid", err)
	}

	finished := time.Now().UTC()
	aborted, err := tasks.NewTaskResult(tasks.TaskResultParams{
		TaskID: "cancel-before-start", Outcome: tasks.OutcomeAbortedBeforeStart,
		Cause: tasks.ResultCauseInvalidPermit, FinishedAt: finished,
		Failure: tasks.FailureInfo{Code: "start_rejected", Message: "cancel won before start"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := held.events.Completed(held.assignment.Permit(), aborted); err != nil {
		t.Fatal(err)
	}

	snapshot := waitState(t, engine, "cancel-before-start", true)
	if snapshot.State() != tasks.LifecycleCancelled || !snapshot.StartedAt().IsZero() || !snapshot.CancelRequested() {
		t.Fatalf("terminal snapshot state=%v started=%v cancel=%v", snapshot.State(), snapshot.StartedAt(), snapshot.CancelRequested())
	}
	result, ok, err := engine.ConsumeResult("cancel-before-start")
	if err != nil || !ok {
		t.Fatalf("ConsumeResult() ok=%v err=%v", ok, err)
	}
	if result.Outcome() != tasks.OutcomeCancelled || result.Cause() != tasks.ResultCauseCancellation || !result.StartedAt().IsZero() {
		t.Fatalf("normalized result outcome=%v cause=%v started=%v", result.Outcome(), result.Cause(), result.StartedAt())
	}
	stats := engine.Stats()
	assertP2Conservation(t, stats)
	if stats.Pools["general"].Idle != 1 || stats.ReadyQueued != 0 || len(stats.Owners) != 0 {
		t.Fatalf("capacity was not fully returned: %+v owners=%+v", stats.Pools["general"], stats.Owners)
	}
}

func TestEngine_WorkerAbortBeforeStartReturnsInventory(t *testing.T) {
	engine, workers, handler, scope := newHoldingEngine(t)
	if _, err := engine.Submit(context.Background(), holdingSpec(t, handler, scope, "worker-abort")); err != nil {
		t.Fatal(err)
	}
	held := <-workers.assigned
	finished := time.Now().UTC()
	result, err := tasks.NewTaskResult(tasks.TaskResultParams{
		TaskID: "worker-abort", Outcome: tasks.OutcomeCancelled, Cause: tasks.ResultCauseEngineShutdown,
		FinishedAt: finished, Failure: tasks.FailureInfo{Code: "engine_shutdown", Message: "worker stopped before start"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := held.events.Completed(held.assignment.Permit(), result); err != nil {
		t.Fatal(err)
	}

	snapshot := waitState(t, engine, "worker-abort", true)
	if snapshot.State() != tasks.LifecycleCancelled || !snapshot.StartedAt().IsZero() {
		t.Fatalf("worker abort snapshot state=%v started=%v", snapshot.State(), snapshot.StartedAt())
	}
	stats := engine.Stats()
	assertP2Conservation(t, stats)
	if stats.Pools["general"].Idle != 1 || stats.Owners["owner"].Reserved != 0 || stats.Owners["owner"].Running != 0 {
		t.Fatalf("worker abort leaked inventory: pool=%+v owner=%+v", stats.Pools["general"], stats.Owners["owner"])
	}
}
