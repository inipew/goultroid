package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type recordedCall struct {
	component string
	action    string
}

type recordingComponent struct {
	name         string
	dependencies []string
	calls        *[]recordedCall
	mu           *sync.Mutex
	failStart    bool
	failStop     bool
	health       ComponentHealth
}

func (c *recordingComponent) Name() string           { return c.name }
func (c *recordingComponent) Dependencies() []string { return c.dependencies }
func (c *recordingComponent) Start(ctx context.Context) error {
	c.mu.Lock()
	*c.calls = append(*c.calls, recordedCall{component: c.name, action: "start"})
	c.mu.Unlock()
	if c.failStart {
		return errors.New("start failure")
	}
	return nil
}
func (c *recordingComponent) Stop(ctx context.Context) error {
	c.mu.Lock()
	*c.calls = append(*c.calls, recordedCall{component: c.name, action: "stop"})
	c.mu.Unlock()
	if c.failStop {
		return errors.New("stop failure")
	}
	return nil
}
func (c *recordingComponent) Health(ctx context.Context) ComponentHealth {
	return c.health
}

func TestRuntime_LifecycleAndOrdering(t *testing.T) {
	r := New()
	if r.State() != StateCreated {
		t.Fatalf("expected initial state created, got %s", r.State())
	}

	var calls []recordedCall
	var mu sync.Mutex

	c1 := &recordingComponent{name: "db", calls: &calls, mu: &mu}
	c2 := &recordingComponent{name: "service", dependencies: []string{"db"}, calls: &calls, mu: &mu}
	c3 := &recordingComponent{name: "app", dependencies: []string{"service"}, calls: &calls, mu: &mu}

	_ = r.Register(c1)
	_ = r.Register(c2)
	_ = r.Register(c3)

	ctx := context.Background()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("runtime start failed: %v", err)
	}

	if r.State() != StateRunning {
		t.Fatalf("expected state running, got %s", r.State())
	}
	if !r.State().IsRunning() {
		t.Fatalf("expected IsRunning() == true")
	}

	// Startup order must be: db, service, app
	mu.Lock()
	if len(calls) != 3 || calls[0].component != "db" || calls[1].component != "service" || calls[2].component != "app" {
		t.Errorf("unexpected startup sequence: %+v", calls)
	}
	calls = nil
	mu.Unlock()

	// Shutdown order must be: app, service, db
	if err := r.Stop(ctx); err != nil {
		t.Fatalf("runtime stop failed: %v", err)
	}

	if r.State() != StateStopped {
		t.Fatalf("expected state stopped, got %s", r.State())
	}

	mu.Lock()
	if len(calls) != 3 || calls[0].component != "app" || calls[1].component != "service" || calls[2].component != "db" {
		t.Errorf("unexpected shutdown sequence: %+v", calls)
	}
	mu.Unlock()

	// Idempotent Stop
	if err := r.Stop(ctx); err != nil {
		t.Errorf("expected subsequent Stop to return nil, got: %v", err)
	}
}

func TestRuntime_StopDoesNotInvokeComponentsAfterDeadline(t *testing.T) {
	r := New()
	var calls []recordedCall
	var mu sync.Mutex

	_ = r.Register(&recordingComponent{name: "first", calls: &calls, mu: &mu})
	_ = r.Register(&recordingComponent{name: "second", dependencies: []string{"first"}, calls: &calls, mu: &mu})
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("runtime start failed: %v", err)
	}

	mu.Lock()
	calls = nil
	mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := r.Stop(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop() error = %v, want context.Canceled", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("components invoked after stop context expired: %+v", calls)
	}
}

func TestRuntime_SingleUseStart(t *testing.T) {
	r := New()
	_ = r.Register(&mockComponent{name: "comp"})

	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("first start failed: %v", err)
	}

	if err := r.Start(context.Background()); err == nil {
		t.Fatalf("expected second start to fail")
	}
}

func TestRuntime_RollbackOnStartupFailure(t *testing.T) {
	r := New()
	var calls []recordedCall
	var mu sync.Mutex

	c1 := &recordingComponent{name: "db", calls: &calls, mu: &mu}
	c2 := &recordingComponent{name: "service", dependencies: []string{"db"}, calls: &calls, mu: &mu, failStart: true}
	c3 := &recordingComponent{name: "app", dependencies: []string{"service"}, calls: &calls, mu: &mu}

	_ = r.Register(c1)
	_ = r.Register(c2)
	_ = r.Register(c3)

	err := r.Start(context.Background())
	if err == nil {
		t.Fatalf("expected startup failure, got nil")
	}

	if r.State() != StateFailed {
		t.Fatalf("expected runtime state failed, got %s", r.State())
	}

	// c1 was started, so it should be rolled back (stopped).
	// c3 was never started.
	mu.Lock()
	c1Stopped := false
	for _, call := range calls {
		if call.component == "db" && call.action == "stop" {
			c1Stopped = true
		}
		if call.component == "app" {
			t.Errorf("component app should never have been invoked: %+v", call)
		}
	}
	mu.Unlock()

	if !c1Stopped {
		t.Fatalf("expected db to be rolled back/stopped after service failed to start")
	}
}

func TestRuntime_HealthAggregation(t *testing.T) {
	r := New()
	c1 := &mockComponent{name: "db", health: ComponentHealth{Status: HealthHealthy}}
	c2 := &mockComponent{name: "cache", health: ComponentHealth{Status: HealthDegraded, Details: "slow"}}
	_ = r.Register(c1)
	_ = r.Register(c2)

	_ = r.Start(context.Background())
	defer r.Stop(context.Background())

	h := r.Health(context.Background())
	if h.Status != HealthDegraded {
		t.Errorf("expected aggregate health degraded, got %s", h.Status)
	}
	if !h.Ready {
		t.Errorf("expected ready true while running")
	}
	if h.Components["db"] != HealthHealthy || h.Components["cache"] != HealthDegraded {
		t.Errorf("unexpected component health: %+v", h.Components)
	}
}
