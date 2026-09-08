package app

import (
	"context"
	"errors"
	"testing"
)

func TestLifecycleTransitions(t *testing.T) {
	l := newLifecycle()
	if got := l.State(); got != LifecycleNew { t.Fatalf("initial state = %v", got) }
	if err := l.beginStart(); err != nil { t.Fatal(err) }
	l.markRunning()
	if !l.quiesce() { t.Fatal("expected quiesce transition") }
	if l.canAccept() { t.Fatal("quiescing lifecycle must reject new work") }
	if l.quiesce() { t.Fatal("second quiesce must be idempotent") }
}

func TestLifecycleShutdownIdempotent(t *testing.T) {
	l := newLifecycle()
	var calls int
	fn := func(context.Context) error { calls++; return nil }
	if err := l.shutdown(context.Background(), fn); err != nil { t.Fatal(err) }
	if err := l.shutdown(context.Background(), fn); err != nil { t.Fatal(err) }
	if calls != 1 { t.Fatalf("shutdown callback called %d times", calls) }
	if l.State() != LifecycleStopped { t.Fatalf("state = %v", l.State()) }
}

func TestLifecycleShutdownError(t *testing.T) {
	l := newLifecycle()
	want := errors.New("shutdown failure")
	if err := l.shutdown(context.Background(), func(context.Context) error { return want }); !errors.Is(err, want) { t.Fatalf("error = %v", err) }
	if l.State() != LifecycleFailed { t.Fatalf("state = %v", l.State()) }
}
