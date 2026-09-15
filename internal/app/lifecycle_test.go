package app

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/runtime"
)

func TestAppLifecycleStateTransitions(t *testing.T) {
	a := &App{shutdownDone: make(chan struct{})}
	if a.LifecycleState() != "new" {
		t.Fatalf("initial state=%s", a.LifecycleState())
	}
	if err := a.beginStart(); err != nil {
		t.Fatal(err)
	}
	if a.LifecycleState() != "starting" {
		t.Fatalf("state=%s", a.LifecycleState())
	}
	a.markRunning()
	if a.LifecycleState() != "running" {
		t.Fatalf("state=%s", a.LifecycleState())
	}
	if !a.beginQuiesce() || a.LifecycleState() != "quiescing" {
		t.Fatalf("failed to quiesce: %s", a.LifecycleState())
	}
	a.markStopping()
	a.markStopped(nil)
	if a.LifecycleState() != "stopped" {
		t.Fatalf("state=%s", a.LifecycleState())
	}
	if a.beginQuiesce() {
		t.Fatal("stopped application must not re-enter quiescing")
	}
}

func TestAppLifecycleStartOnlyOnce(t *testing.T) {
	a := &App{shutdownDone: make(chan struct{})}
	if err := a.beginStart(); err != nil {
		t.Fatal(err)
	}
	if err := a.beginStart(); err == nil {
		t.Fatal("second start should fail")
	}
}

func TestAppShutdown_CapturesRuntimeError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-canceled

	rt := runtime.New()
	a := &App{
		shutdownDone: make(chan struct{}),
		runtime:      rt,
	}

	err := a.Shutdown(ctx)
	if err == nil {
		t.Fatal("expected error when context is pre-canceled, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled wrapped in err, got %v", err)
	}
}
