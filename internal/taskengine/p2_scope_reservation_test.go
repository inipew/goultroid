package taskengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func scopeSpec(t *testing.T, h *engineHarness, id tasks.TaskID, scope tasks.ScopeIdentity) tasks.WorkSpec {
	t.Helper()
	payload, err := tasks.NewPayloadRef("test", 1, []byte(id))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := tasks.NewWorkSpec(tasks.WorkSpecParams{
		ID: id, Scope: scope, QuotaOwner: "owner", Pool: "general",
		Class: tasks.PriorityNormal, Cause: tasks.CauseManual,
		Handler: h.handler, Input: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func requireScopeRejected(t *testing.T, engine *Engine, spec tasks.WorkSpec) {
	t.Helper()
	_, err := engine.Submit(context.Background(), spec)
	if err == nil {
		t.Fatalf("Submit(%s) unexpectedly accepted a closed scope", spec.ID())
	}
	var admissionErr *tasks.AdmissionError
	if !errors.As(err, &admissionErr) || admissionErr.Reason != tasks.RejectScopeClosed {
		t.Fatalf("Submit(%s) error=%v want scope_closed", spec.ID(), err)
	}
}

func TestEngine_CloseScopeFencesAllOlderGenerations(t *testing.T) {
	started := make(chan struct{}, 1)
	cfg := engineConfig()
	cfg.Pools["general"] = PoolLimits{Workers: 1, MaxWaiting: 64, MaxWaitingBytes: 1 << 20}
	cfg.DefaultOwner.MaxActive = 1
	h := newEngineHarness(t, cfg, map[string]tasks.HandlerFunc{
		"test.run": func(ctx context.Context, payload tasks.PayloadRef) (tasks.ResultRef, error) {
			if string(payload.Data()) == "gen1" {
				started <- struct{}{}
				<-ctx.Done()
				return tasks.ResultRef{}, ctx.Err()
			}
			return tasks.ResultRef{}, nil
		},
	})

	scope1, err := tasks.NewScopeIdentity("scope:generation", 1)
	if err != nil {
		t.Fatal(err)
	}
	scope2, err := tasks.NewScopeIdentity("scope:generation", 2)
	if err != nil {
		t.Fatal(err)
	}
	scope3, err := tasks.NewScopeIdentity("scope:generation", 3)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := h.engine.Submit(context.Background(), scopeSpec(t, h, "gen1", scope1)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("generation 1 did not start")
	}
	if _, err := h.engine.Submit(context.Background(), scopeSpec(t, h, "gen2", scope2)); err != nil {
		t.Fatal(err)
	}

	cancelled, err := h.engine.CloseScope(scope2)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled != 2 {
		t.Fatalf("CloseScope(gen2) cancelled=%d want 2", cancelled)
	}
	if got := waitState(t, h.engine, "gen1", true); got.State() != tasks.LifecycleCancelled {
		t.Fatalf("gen1 state=%v want cancelled", got.State())
	}
	if got := waitState(t, h.engine, "gen2", true); got.State() != tasks.LifecycleCancelled {
		t.Fatalf("gen2 state=%v want cancelled", got.State())
	}

	requireScopeRejected(t, h.engine, scopeSpec(t, h, "late-gen1", scope1))
	requireScopeRejected(t, h.engine, scopeSpec(t, h, "late-gen2", scope2))

	if cancelled, err := h.engine.CloseScope(scope1); err != nil || cancelled != 0 {
		t.Fatalf("stale CloseScope(gen1) cancelled=%d err=%v", cancelled, err)
	}
	if _, err := h.engine.Submit(context.Background(), scopeSpec(t, h, "gen3", scope3)); err != nil {
		t.Fatalf("new generation was fenced by stale close: %v", err)
	}
	if got := waitState(t, h.engine, "gen3", true); got.State() != tasks.LifecycleSucceeded {
		t.Fatalf("gen3 state=%v want succeeded", got.State())
	}
	assertP2Conservation(t, h.engine.Stats())
}

func TestEngine_PhysicalReservationCannotBeStolenByOrdinarySubmit(t *testing.T) {
	engine, workers, handler, scope := newHoldingEngine(t)
	if _, err := engine.Submit(context.Background(), holdingSpec(t, handler, scope, "reserved")); err != nil {
		t.Fatal(err)
	}
	first := <-workers.assigned
	if first.assignment.Permit().TaskID() != "reserved" {
		t.Fatalf("first permit task=%s want reserved", first.assignment.Permit().TaskID())
	}

	if _, err := engine.Submit(context.Background(), holdingSpec(t, handler, scope, "ordinary")); err != nil {
		t.Fatal(err)
	}
	select {
	case stolen := <-workers.assigned:
		t.Fatalf("ordinary task stole reserved slot with permit for %s", stolen.assignment.Permit().TaskID())
	case <-time.After(25 * time.Millisecond):
	}

	startedAt := time.Now().UTC()
	if err := first.events.Started(first.assignment.Permit(), startedAt); err != nil {
		t.Fatal(err)
	}
	result, err := tasks.NewTaskResult(tasks.TaskResultParams{
		TaskID: "reserved", Outcome: tasks.OutcomeSucceeded, Cause: tasks.ResultCauseNone,
		StartedAt: startedAt, FinishedAt: startedAt.Add(time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.events.Completed(first.assignment.Permit(), result); err != nil {
		t.Fatal(err)
	}

	var second heldAssignment
	select {
	case second = <-workers.assigned:
	case <-time.After(time.Second):
		t.Fatal("ordinary task was not assigned after permit release")
	}
	if second.assignment.Permit().TaskID() != "ordinary" {
		t.Fatalf("second permit task=%s want ordinary", second.assignment.Permit().TaskID())
	}
	if second.assignment.Permit().DispatchEpoch() <= first.assignment.Permit().DispatchEpoch() {
		t.Fatalf("dispatch epoch did not advance: first=%d second=%d", first.assignment.Permit().DispatchEpoch(), second.assignment.Permit().DispatchEpoch())
	}

	secondStarted := time.Now().UTC()
	if err := second.events.Started(second.assignment.Permit(), secondStarted); err != nil {
		t.Fatal(err)
	}
	secondResult, err := tasks.NewTaskResult(tasks.TaskResultParams{
		TaskID: "ordinary", Outcome: tasks.OutcomeSucceeded, Cause: tasks.ResultCauseNone,
		StartedAt: secondStarted, FinishedAt: secondStarted.Add(time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.events.Completed(second.assignment.Permit(), secondResult); err != nil {
		t.Fatal(err)
	}
	if got := waitState(t, engine, "ordinary", true); got.State() != tasks.LifecycleSucceeded {
		t.Fatalf("ordinary state=%v want succeeded", got.State())
	}
	assertP2Conservation(t, engine.Stats())
}
