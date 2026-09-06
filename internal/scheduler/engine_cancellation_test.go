package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestEngineCancelActiveJobCancelsEveryLocalClaim(t *testing.T) {
	engine := NewEngine(nil, nil, nil, nil, zap.NewNop())

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()

	engine.registerActiveJob(42, "claim-a", cancel1)
	engine.registerActiveJob(42, "claim-b", cancel2)
	engine.cancelActiveJob(42)

	select {
	case <-ctx1.Done():
	case <-time.After(time.Second):
		t.Fatal("first active claim was not cancelled")
	}
	select {
	case <-ctx2.Done():
	case <-time.After(time.Second):
		t.Fatal("second active claim was not cancelled")
	}

	engine.unregisterActiveJob(42, "claim-a")
	engine.unregisterActiveJob(42, "claim-b")
	if len(engine.activeJobs) != 0 {
		t.Fatalf("active job registry not cleaned up: %#v", engine.activeJobs)
	}
}

func TestEngineActiveJobRegistrySupportsIndependentClaims(t *testing.T) {
	engine := NewEngine(nil, nil, nil, nil, zap.NewNop())
	var cancelled atomic.Int32

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	cancelAWrapper := func() { cancelled.Add(1); cancelA() }
	cancelBWrapper := func() { cancelled.Add(1); cancelB() }

	engine.registerActiveJob(1, "a", cancelAWrapper)
	engine.registerActiveJob(2, "b", cancelBWrapper)
	engine.cancelActiveJob(1)

	select {
	case <-ctxA.Done():
	case <-time.After(time.Second):
		t.Fatal("job 1 was not cancelled")
	}
	select {
	case <-ctxB.Done():
		t.Fatal("job 2 was cancelled by another job's cancellation")
	default:
	}
	if got := cancelled.Load(); got != 1 {
		t.Fatalf("cancelled callback count = %d, want 1", got)
	}

	engine.unregisterActiveJob(1, "a")
	engine.unregisterActiveJob(2, "b")
	cancelB()
}
