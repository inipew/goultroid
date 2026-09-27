package taskengine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestDirectCommitPanicYieldsRecoveryRequired(t *testing.T) {
	pump := &stubPump{mode: stubReject}
	e := durableTestEngine(t, Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 1, BacklogLimit: 4, PayloadBudget: 1 << 20},
		},
		ResultCapacity: 4, MaxTerminalRetained: 4, DecisionTimeout: time.Second,
	}, pump)
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID: "direct-panic", QuotaOwner: "owner", Pool: "general",
		Handler: func(context.Context) error { return nil },
		Commit:  func(context.Context, tasks.TaskResult) error { panic("commit exploded") },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := ticket.Wait(ctx)
	if err != nil {
		t.Fatalf("panic commit did not resolve: %v", err)
	}
	if res.Cause != tasks.CausePersistenceFailure {
		t.Fatalf("cause = %s, want persistence_failure", res.Cause)
	}
	if !strings.Contains(res.Failure.Message, "durability direct commit panic") {
		t.Fatalf("failure = %q", res.Failure.Message)
	}
	snap, ok := e.Snapshot("direct-panic")
	if !ok || snap.State != tasks.StateRecoveryRequired {
		t.Fatalf("snapshot = %+v found=%v", snap, ok)
	}
}

func TestForceStopReleasesInFlightResources(t *testing.T) {
	e := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"p": {Concurrency: 1, BacklogLimit: 2, PayloadBudget: 1 << 20},
		},
		ResultCapacity: 2, ResourceCapacities: map[string]int64{"process": 1},
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID: "forced-resource", QuotaOwner: "owner", Pool: "p",
		Resources: []tasks.ResourceRequirement{{Name: "process", Amount: 1}},
		Handler:   func(context.Context) error { close(started); <-release; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("resource task did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err = e.ForceStop(ctx)
	cancel()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ForceStop() error = %v", err)
	}
	if got := e.resourceUsed["process"]; got != 0 {
		t.Fatalf("forced stop retained resource usage %d", got)
	}
	res, waitErr := ticket.Wait(context.Background())
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	if res.Cause != tasks.CauseShutdown {
		t.Fatalf("forced result = %+v", res)
	}
}
