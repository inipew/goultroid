package runtime

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
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

type blockingHealthComponent struct {
	recordingComponent
	healthStarted chan struct{}
	releaseHealth chan struct{}
}

type callbackComponent struct {
	name    string
	deps    []string
	startFn func(context.Context) error
	stopFn  func(context.Context) error
}

type phasedComponent struct {
	recordingComponent
}

func (c *phasedComponent) Quiesce(context.Context) error {
	c.mu.Lock()
	*c.calls = append(*c.calls, recordedCall{component: c.name, action: "quiesce"})
	c.mu.Unlock()
	return nil
}
func (c *phasedComponent) Drain(context.Context) error {
	c.mu.Lock()
	*c.calls = append(*c.calls, recordedCall{component: c.name, action: "drain"})
	c.mu.Unlock()
	return nil
}

func (c *callbackComponent) Name() string           { return c.name }
func (c *callbackComponent) Dependencies() []string { return c.deps }
func (c *callbackComponent) Start(ctx context.Context) error {
	if c.startFn != nil {
		return c.startFn(ctx)
	}
	return nil
}
func (c *callbackComponent) Stop(ctx context.Context) error {
	if c.stopFn != nil {
		return c.stopFn(ctx)
	}
	return nil
}
func (c *callbackComponent) Health(context.Context) ComponentHealth {
	return ComponentHealth{Status: HealthHealthy}
}

func (c *blockingHealthComponent) Health(context.Context) ComponentHealth {
	close(c.healthStarted)
	<-c.releaseHealth
	return ComponentHealth{Status: HealthHealthy}
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

func TestRuntime_StopCanceledCallerDoesNotCancelShutdown(t *testing.T) {
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

	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("shutdown did not complete: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0].component != "second" || calls[1].component != "first" {
		t.Fatalf("shutdown did not continue in reverse order: %+v", calls)
	}
}

func TestRuntime_StopCallerTimeoutDoesNotCancelShutdown(t *testing.T) {
	release := make(chan struct{})
	stopped := make(chan struct{})
	r := New()
	r.stopTimeout = time.Second
	_ = r.Register(&callbackComponent{name: "slow", stopFn: func(context.Context) error {
		<-release
		close(stopped)
		return nil
	}})
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := r.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() error = %v, want caller deadline", err)
	}
	close(release)
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("shutdown did not continue after caller timeout: %v", err)
	}
	<-stopped
	if r.State() != StateStopped {
		t.Fatalf("state = %s, want stopped", r.State())
	}
}

func TestRuntime_ShutdownFailureSetsFailed(t *testing.T) {
	r := New()
	_ = r.Register(&callbackComponent{name: "broken", stopFn: func(context.Context) error { return errors.New("boom") }})
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(context.Background()); err == nil {
		t.Fatal("Stop() succeeded, want error")
	}
	if r.State() != StateFailed {
		t.Fatalf("state = %s, want failed", r.State())
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

func TestRuntime_HealthProbeDoesNotBlockStop(t *testing.T) {
	r := New()
	var calls []recordedCall
	var mu sync.Mutex
	comp := &blockingHealthComponent{
		recordingComponent: recordingComponent{name: "slow-health", calls: &calls, mu: &mu},
		healthStarted:      make(chan struct{}),
		releaseHealth:      make(chan struct{}),
	}
	if err := r.Register(comp); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	healthDone := make(chan struct{})
	go func() {
		_ = r.Health(context.Background())
		close(healthDone)
	}()
	<-comp.healthStarted
	stopDone := make(chan error, 1)
	go func() { stopDone <- r.Stop(context.Background()) }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow health probe blocked lifecycle stop")
	}
	close(comp.releaseHealth)
	<-healthDone
}

func TestRuntime_ComponentCallbacksDoNotRunUnderCoordinatorLock(t *testing.T) {
	r := New()
	comp := &callbackComponent{name: "callback"}
	comp.startFn = func(context.Context) error {
		if r.Component("callback") == nil {
			return errors.New("component lookup failed during start")
		}
		return nil
	}
	comp.stopFn = func(context.Context) error {
		_ = r.Uptime()
		return nil
	}
	if err := r.Register(comp); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntime_StopUsesQuiesceDrainStopPhases(t *testing.T) {
	r := New()
	var calls []recordedCall
	var mu sync.Mutex
	db := &phasedComponent{recordingComponent{name: "db", calls: &calls, mu: &mu}}
	app := &phasedComponent{recordingComponent{name: "app", dependencies: []string{"db"}, calls: &calls, mu: &mu}}
	if err := r.Register(db); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(app); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	calls = nil
	mu.Unlock()
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []recordedCall{
		{component: "app", action: "quiesce"}, {component: "app", action: "drain"},
		{component: "db", action: "quiesce"}, {component: "db", action: "drain"},
		{component: "app", action: "stop"}, {component: "db", action: "stop"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("shutdown phases = %+v, want %+v", calls, want)
	}
}

func TestRuntime_StartPropagatesOperationCancellationAndBoundsRollback(t *testing.T) {
	r := New()
	var calls []recordedCall
	var mu sync.Mutex
	if err := r.Register(&recordingComponent{name: "started", calls: &calls, mu: &mu}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&callbackComponent{name: "blocking", deps: []string{"started"}, startFn: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := r.Start(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start() error = %v, want deadline exceeded", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[1].action != "stop" {
		t.Fatalf("started component was not rolled back: %+v", calls)
	}
}

func TestRuntime_ShutdownReportMetrics(t *testing.T) {
	r := New()
	var calls []recordedCall
	var mu sync.Mutex
	c1 := &recordingComponent{name: "comp1", calls: &calls, mu: &mu}
	c2 := &phasedComponent{recordingComponent{name: "comp2", calls: &calls, mu: &mu}}

	if err := r.Register(c1); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(c2); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	report := r.LastShutdownReport()
	if report.TotalDuration <= 0 {
		t.Fatalf("expected TotalDuration > 0, got %v", report.TotalDuration)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("expected 0 errors in report, got %v", report.Errors)
	}
	if len(report.Phases) == 0 {
		t.Fatal("expected non-empty phases in report")
	}

	phaseMap := make(map[string]bool)
	for _, p := range report.Phases {
		if p.Duration < 0 {
			t.Errorf("phase %s duration %v must be >= 0", p.Phase, p.Duration)
		}
		phaseMap[string(p.Phase)+":"+p.Component] = true
	}

	// comp2 has quiesce, drain, stop
	if !phaseMap["quiesce:comp2"] {
		t.Error("missing quiesce:comp2 in report")
	}
	if !phaseMap["drain:comp2"] {
		t.Error("missing drain:comp2 in report")
	}
	if !phaseMap["stop:comp2"] {
		t.Error("missing stop:comp2 in report")
	}
	// comp1 has stop
	if !phaseMap["stop:comp1"] {
		t.Error("missing stop:comp1 in report")
	}
}
