package app

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
)

type quiesceTrackingComp struct {
	mockResourceComp
	quiesceCalls int
}

func (q *quiesceTrackingComp) Quiesce(ctx context.Context) error {
	q.quiesceCalls++
	return nil
}

func TestAppShutdown_QuiescesThroughRuntimeWithoutDuplicateInvocation(t *testing.T) {
	rt := runtime.New()
	comp := &quiesceTrackingComp{
		mockResourceComp: mockResourceComp{name: "tracked-comp"},
	}
	_ = rt.Register(comp)

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime Start failed: %v", err)
	}

	logger, _, _ := initLogger("error")
	a := &App{
		logger:       logger,
		runtime:      rt,
		shutdownDone: make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := a.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	if comp.quiesceCalls != 1 {
		t.Fatalf("expected component to be quiesced exactly once, got %d", comp.quiesceCalls)
	}
	if a.LifecycleState() != "stopped" {
		t.Fatalf("expected state stopped, got %s", a.LifecycleState())
	}
}
