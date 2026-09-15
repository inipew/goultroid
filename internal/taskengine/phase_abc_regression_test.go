package taskengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

// A/P0: once the coordinator has published ACCEPT, producer cancellation must
// not hide that decision while the reply is still in flight.
func TestSubmitHandoffCancellationCannotHideAcceptedDecision(t *testing.T) {
	e := NewEngine(Config{DecisionTimeout: time.Second})
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	inbox := make(chan engineRequest, 1)
	e.mu.Lock()
	e.inbox = inbox
	e.rootCtx = rootCtx
	e.decisionTimeout = time.Second
	e.mu.Unlock()

	accepted := make(chan struct{})
	releaseReply := make(chan struct{})
	go func() {
		req := <-inbox
		if !req.decision.decide(decisionAccepted) {
			t.Errorf("coordinator lost admission decision")
			return
		}
		close(accepted)
		<-releaseReply
		req.reply <- engineReply{}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := e.sendSubmitControl(ctx, tasks.WorkSpec{ID: "linearized"})
		result <- err
	}()
	<-accepted
	cancel()

	select {
	case err := <-result:
		t.Fatalf("Submit returned before coordinator reply after ACCEPT: %v", err)
	case <-time.After(30 * time.Millisecond):
		// Expected: cancellation lost the decision CAS and cannot mask ACCEPT.
	}
	close(releaseReply)
	if err := <-result; err != nil {
		t.Fatalf("accepted decision was hidden by cancellation: %v", err)
	}
}

// B/P0: mutable/opaque object graphs are rejected rather than being charged a
// fake constant estimate and retained by caller-owned reference.
func TestAdmissionRejectsOpaqueMutableInput(t *testing.T) {
	e := auditEngine(t)
	_, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID: "opaque", Pool: "a", QuotaOwner: "owner",
		Input: map[string][]byte{"huge": make([]byte, 1<<20)},
		Handler: func(context.Context) error { return nil },
	})
	if !errors.Is(err, tasks.ErrUnsupportedPayload) {
		t.Fatalf("expected ErrUnsupportedPayload, got %v", err)
	}
	if _, ok := e.Snapshot("opaque"); ok {
		t.Fatal("rejected opaque payload entered registry")
	}
}

// B/P0: callback capacity is reserved at admission. Saturation rejects the
// next callback-bearing task instead of spawning an unbounded rescue goroutine.
func TestCompletionDeliverySaturationBackpressuresAdmission(t *testing.T) {
	e := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{"p": {Concurrency: 1, BacklogLimit: 8, PayloadBudget: 1 << 20}},
		ResultCapacity: 8, DeliveryConcurrency: 1, DeliveryQueueCap: 1,
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())

	release := make(chan struct{})
	first, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID: "cb-first", Pool: "p", QuotaOwner: "owner",
		Handler: func(context.Context) error { <-release; return nil },
		OnComplete: func(tasks.TaskResult) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Submit(context.Background(), tasks.WorkSpec{
		ID: "cb-second", Pool: "p", QuotaOwner: "owner",
		Handler: func(context.Context) error { return nil },
		OnComplete: func(tasks.TaskResult) {},
	})
	if !errors.Is(err, tasks.ErrDeliveryBackpressure) {
		t.Fatalf("expected delivery backpressure, got %v", err)
	}
	close(release)
	if _, err := first.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A/P0: completion is fenced by the exact physical permit/epoch, not TaskID.
func TestStaleWorkerCompletionCannotMutateReusedTaskID(t *testing.T) {
	e := NewEngine(Config{Pools: map[tasks.PoolID]PoolEngineConfig{"p": {Concurrency: 1, BacklogLimit: 8}}})
	current := newPermit("p", 0, 2, "same-id", 20, nil)
	stale := newPermit("p", 0, 1, "same-id", 10, nil)
	rec := &taskRecord{
		spec: tasks.WorkSpec{ID: "same-id", Pool: "p", QuotaOwner: "owner"},
		state: tasks.StateRunning, permit: current, poolGeneration: 2, dispatchEpoch: 20,
	}
	e.registry["same-id"] = rec

	e.applyWorkerCompleted(tasks.TaskResult{TaskID: "same-id", Outcome: tasks.OutcomeCompleted}, stale)
	if rec.state != tasks.StateRunning {
		t.Fatalf("stale completion changed state to %s", rec.state)
	}
	if rec.result.Outcome != "" {
		t.Fatalf("stale completion wrote result: %+v", rec.result)
	}
}

// C/P1: a blocked user completion callback must not consume durability ack
// workers. Durable tickets settle through their dedicated bounded lane.
func TestDurabilityLaneIndependentFromCompletionCallbacks(t *testing.T) {
	e := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{"p": {Concurrency: 2, BacklogLimit: 8, PayloadBudget: 1 << 20}},
		ResultCapacity: 8, DeliveryConcurrency: 1, DeliveryQueueCap: 2,
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())

	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	callbackTicket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID: "blocking-callback", Pool: "p", QuotaOwner: "owner",
		Handler: func(context.Context) error { return nil },
		OnComplete: func(tasks.TaskResult) {
			close(callbackStarted)
			<-releaseCallback
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callbackTicket.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-callbackStarted

	durable, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID: "durable", Pool: "p", QuotaOwner: "owner",
		Handler: func(context.Context) error { return nil },
		Commit: func(context.Context, tasks.TaskResult) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := durable.Wait(ctx)
	if err != nil {
		t.Fatalf("durability stalled behind callback worker: %v", err)
	}
	if !res.IsSuccess() {
		t.Fatalf("durable task failed: %+v", res)
	}
	close(releaseCallback)
}
