package app

import (
	"testing"
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
