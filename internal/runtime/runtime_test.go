package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
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
	name     string
	deps     []string
	startFn  func(context.Context) error
	stopFn   func(context.Context) error
	healthFn func(context.Context) ComponentHealth
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
func (c *callbackComponent) Health(ctx context.Context) ComponentHealth {
	if c.healthFn != nil {
		return c.healthFn(ctx)
	}
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

func TestRuntime_StopWithinMakesOwnerContextAuthoritative(t *testing.T) {
	r := New()
	r.stopTimeout = time.Second

	stopSawDeadline := make(chan struct{})
	if err := r.Register(&callbackComponent{name: "slow", stopFn: func(ctx context.Context) error {
		<-ctx.Done()
		close(stopSawDeadline)
		return ctx.Err()
	}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_ = r.StopWithin(ctx)

	select {
	case <-stopSawDeadline:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("runtime teardown did not observe lifecycle-owner deadline")
	}
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("StopWithin ignored owner budget; elapsed=%v", elapsed)
	}
	if err := r.Stop(context.Background()); err == nil {
		t.Fatal("expected shutdown error after component stop deadline")
	}
}

type optionalComponent struct {
	mockComponent
	isCritical bool
}

func (c *optionalComponent) IsCritical() bool {
	return c.isCritical
}

func TestRuntime_OptionalComponentStartFailureDegradesHealthAndRecordsDiagnostics(t *testing.T) {
	r := New()

	db := &mockComponent{name: "db"}
	assistant := &optionalComponent{
		mockComponent: mockComponent{
			name:     "assistant",
			startErr: errors.New("bot token revoked"),
		},
		isCritical: false,
	}

	_ = r.Register(db)
	_ = r.Register(assistant)

	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("expected optional startup failure to permit runtime start, got %v", err)
	}
	if r.State() != StateRunning {
		t.Fatalf("state = %s, want running", r.State())
	}

	failures := r.StartupFailures()
	if len(failures) != 1 || failures["assistant"] == nil {
		t.Fatalf("expected assistant in StartupFailures, got %+v", failures)
	}

	health := r.Health(context.Background())
	if health.Status != HealthDegraded {
		t.Fatalf("health status = %s, want degraded", health.Status)
	}
	if health.Components["assistant"] != HealthDegraded {
		t.Fatalf("assistant component health = %s, want degraded", health.Components["assistant"])
	}
	if health.Components["db"] != HealthHealthy {
		t.Fatalf("db component health = %s, want healthy", health.Components["db"])
	}

	foundReason := false
	for _, reason := range health.Reasons {
		if reason == "runtime:optional-component-failed:assistant" {
			foundReason = true
			break
		}
	}
	if !foundReason {
		t.Fatalf("expected runtime:optional-component-failed:assistant in reasons, got %+v", health.Reasons)
	}

	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("stop error = %v", err)
	}
	if assistant.stopCalled {
		t.Fatal("component that failed start should not be invoked during shutdown")
	}
	if !db.stopCalled {
		t.Fatal("started component db was not stopped during shutdown")
	}
}

func TestRuntime_StartupCancellationByLastComponentRollsBackAndFails(t *testing.T) {
	r := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var firstStopped bool
	var secondStopped bool

	first := &callbackComponent{
		name: "first",
		stopFn: func(context.Context) error {
			firstStopped = true
			return nil
		},
	}
	// The second (and last) component cancels caller ctx from inside Start, but returns nil
	second := &callbackComponent{
		name: "second",
		deps: []string{"first"},
		startFn: func(context.Context) error {
			cancel()
			return nil
		},
		stopFn: func(context.Context) error {
			secondStopped = true
			return nil
		},
	}

	_ = r.Register(first)
	_ = r.Register(second)

	err := r.Start(ctx)
	if err == nil {
		t.Fatal("expected Start() to fail when caller context is cancelled by component, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected error wrapping context.Canceled, got %v", err)
	}

	if r.State() == StateRunning {
		t.Fatal("runtime must not transition to StateRunning after startup cancellation")
	}
	if r.State() != StateFailed {
		t.Fatalf("runtime state = %s, want %s", r.State(), StateFailed)
	}

	if !firstStopped {
		t.Fatal("previously started component 'first' was not rolled back")
	}
	if !secondStopped {
		t.Fatal("last component 'second' which succeeded in Start was not rolled back")
	}
}

func TestRuntime_StartupDeadlineExceededPostLoopRollsBack(t *testing.T) {
	r := New()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	var firstStopped bool

	first := &callbackComponent{
		name: "first",
		stopFn: func(context.Context) error {
			firstStopped = true
			return nil
		},
	}
	// Component simulates taking long enough that caller deadline expires
	second := &callbackComponent{
		name: "second",
		deps: []string{"first"},
		startFn: func(context.Context) error {
			time.Sleep(30 * time.Millisecond)
			return nil
		},
	}

	_ = r.Register(first)
	_ = r.Register(second)

	err := r.Start(ctx)
	if err == nil {
		t.Fatal("expected Start() to fail when caller deadline is exceeded, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected error wrapping context.DeadlineExceeded, got %v", err)
	}
	if r.State() != StateFailed {
		t.Fatalf("runtime state = %s, want %s", r.State(), StateFailed)
	}
	if !firstStopped {
		t.Fatal("component 'first' was not rolled back")
	}
}

func TestRuntime_HealthSlowReporterBoundedAndPreservesUnknown(t *testing.T) {
	r := New()
	slow := &callbackComponent{
		name: "slow-reporter",
		healthFn: func(ctx context.Context) ComponentHealth {
			time.Sleep(500 * time.Millisecond)
			return ComponentHealth{Status: HealthHealthy}
		},
	}
	_ = r.Register(slow)
	_ = r.Start(context.Background())
	defer r.Stop(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	h := r.Health(ctx)
	duration := time.Since(start)

	if duration >= 400*time.Millisecond {
		t.Fatalf("Health() blocked for %v, want < 400ms", duration)
	}
	if h.Components["slow-reporter"] != HealthUnknown {
		t.Fatalf("slow reporter health = %q, want %q", h.Components["slow-reporter"], HealthUnknown)
	}
	if h.Ready {
		t.Fatal("runtime must not report Ready=true when critical component probe timed out")
	}
	if h.Status != HealthDegraded {
		t.Fatalf("health status = %q, want degraded", h.Status)
	}
	var foundReason bool
	for _, reason := range h.Reasons {
		if reason == "slow-reporter:probe-timeout" {
			foundReason = true
			break
		}
	}
	if !foundReason {
		t.Fatalf("missing probe-timeout reason, got: %v", h.Reasons)
	}
}

func TestRuntime_HealthCriticalFailureMakesNotReady(t *testing.T) {
	r := New()
	crit := &callbackComponent{
		name: "crit-service",
		healthFn: func(ctx context.Context) ComponentHealth {
			return ComponentHealth{
				Status:  HealthUnhealthy,
				Details: "database-connection-lost",
			}
		},
	}
	_ = r.Register(crit)
	_ = r.Start(context.Background())
	defer r.Stop(context.Background())

	h := r.Health(context.Background())
	if h.Ready {
		t.Fatal("runtime must report Ready=false when critical component is unhealthy")
	}
	if h.Status != HealthUnhealthy {
		t.Fatalf("runtime status = %q, want %q", h.Status, HealthUnhealthy)
	}
	if h.Components["crit-service"] != HealthUnhealthy {
		t.Fatalf("crit-service status = %q, want %q", h.Components["crit-service"], HealthUnhealthy)
	}
	var foundReason bool
	for _, reason := range h.Reasons {
		if reason == "crit-service:database-connection-lost" {
			foundReason = true
			break
		}
	}
	if !foundReason {
		t.Fatalf("missing reason, got: %v", h.Reasons)
	}
}

func TestRuntime_HealthOptionalFailureDegradesWithoutMakingNotReady(t *testing.T) {
	r := New()
	crit := &callbackComponent{
		name: "crit-service",
		healthFn: func(ctx context.Context) ComponentHealth {
			return ComponentHealth{Status: HealthHealthy}
		},
	}
	opt := &optionalComponent{
		mockComponent: mockComponent{
			name: "opt-assistant",
			health: ComponentHealth{
				Status:  HealthUnhealthy,
				Details: "assistant-token-expired",
			},
		},
		isCritical: false,
	}
	_ = r.Register(crit)
	_ = r.Register(opt)
	_ = r.Start(context.Background())
	defer r.Stop(context.Background())

	h := r.Health(context.Background())
	if !h.Ready {
		t.Fatal("runtime must remain Ready=true when only an optional component fails")
	}
	if h.Status != HealthDegraded {
		t.Fatalf("runtime status = %q, want %q", h.Status, HealthDegraded)
	}
	if h.Components["opt-assistant"] != HealthUnhealthy {
		t.Fatalf("opt-assistant status = %q, want %q", h.Components["opt-assistant"], HealthUnhealthy)
	}
	var foundReason bool
	for _, reason := range h.Reasons {
		if reason == "opt-assistant:assistant-token-expired" {
			foundReason = true
			break
		}
	}
	if !foundReason {
		t.Fatalf("missing reason, got: %v", h.Reasons)
	}
}

func TestRuntime_HealthCancelledContextReportsHonestUnknown(t *testing.T) {
	r := New()
	comp := &callbackComponent{name: "any-service"}
	_ = r.Register(comp)
	_ = r.Start(context.Background())
	defer r.Stop(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	h := r.Health(ctx)
	if h.Components["any-service"] != HealthUnknown {
		t.Fatalf("component health = %q, want %q", h.Components["any-service"], HealthUnknown)
	}
	if h.Ready {
		t.Fatal("runtime must not report Ready=true when probe context was cancelled")
	}
}

func TestRuntime_HealthReporterPanicIsContained(t *testing.T) {
	r := New()
	panicker := &callbackComponent{
		name: "panicking-service",
		healthFn: func(ctx context.Context) ComponentHealth {
			panic("unexpected internal panic in health probe")
		},
	}
	_ = r.Register(panicker)
	_ = r.Start(context.Background())
	defer r.Stop(context.Background())

	h := r.Health(context.Background())
	if h.Ready {
		t.Fatal("runtime must report Ready=false when component health panics")
	}
	if h.Status != HealthUnhealthy {
		t.Fatalf("runtime status = %q, want %q", h.Status, HealthUnhealthy)
	}
	if h.Components["panicking-service"] != HealthUnhealthy {
		t.Fatalf("panicker status = %q, want %q", h.Components["panicking-service"], HealthUnhealthy)
	}
	var foundReason bool
	for _, reason := range h.Reasons {
		if reason == "panicking-service:panic" {
			foundReason = true
			break
		}
	}
	if !foundReason {
		t.Fatalf("missing panic reason, got: %v", h.Reasons)
	}
}

func TestSanitizeHealthReason_UntrustedInputs(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "valid-name", want: "valid-name"},
		{input: "Component With Spaces (10/20)", want: "component-with-spaces"},
		{input: "https://api.telegram.org/bot1234:ABC?token=secret", want: "https-api-telegram-org-bot1234-abc"},
		{input: "../../etc/passwd", want: "etc-passwd"},
		{input: "component_with_special$#@%*chars", want: "component-with-special-chars"},
		{input: "a-very-long-component-name-that-exceeds-the-maximum-bounded-length-allowed-by-sanitizer", want: "a-very-long-component-name-that-exceeds-the-maxi"},
		{input: "   ", want: "unknown"},
		{input: "???", want: "unknown"},
	}

	for _, tc := range tests {
		got := sanitizeHealthReason(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeHealthReason(%q) = %q, want %q", tc.input, got, tc.want)
		}
		if len(got) > 48 {
			t.Errorf("sanitizeHealthReason(%q) length %d > 48", tc.input, len(got))
		}
	}
}

func TestRuntime_Diagnostics(t *testing.T) {
	r := New()

	db := &mockComponent{name: "db"}
	sensitiveErrMsg := "bot token revoked: https://api.telegram.org/bot123456789:ABCDEF1234567890abcdef1234567890/getMe token=secret123"
	assistant := &optionalComponent{
		mockComponent: mockComponent{
			name:     "assistant",
			startErr: errors.New(sensitiveErrMsg),
		},
		isCritical: false,
	}

	_ = r.Register(db)
	_ = r.Register(assistant)

	// 1. Diagnostics before Start
	initDiag := r.Diagnostics()
	if initDiag.State != StateCreated {
		t.Fatalf("initial state = %s, want %s", initDiag.State, StateCreated)
	}
	if len(initDiag.Components) != 2 {
		t.Fatalf("initial components len = %d, want 2", len(initDiag.Components))
	}
	if len(initDiag.StartupFailures) != 0 {
		t.Fatalf("initial startup failures len = %d, want 0", len(initDiag.StartupFailures))
	}
	for _, c := range initDiag.Components {
		if c.State != "registered" {
			t.Errorf("component %q initial state = %q, want registered", c.Name, c.State)
		}
		if c.Status != "" {
			t.Errorf("unstarted component %q must not report status healthy, got status = %q", c.Name, c.Status)
		}
	}

	// 2. Start runtime (optional component fails, db succeeds)
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("expected runtime start to succeed with optional failure: %v", err)
	}
	if r.State() != StateRunning {
		t.Fatalf("runtime state = %s, want %s", r.State(), StateRunning)
	}

	runDiag := r.Diagnostics()
	if runDiag.State != StateRunning {
		t.Fatalf("running diag state = %s, want %s", runDiag.State, StateRunning)
	}
	if len(runDiag.StartupFailures) != 1 {
		t.Fatalf("running startup failures len = %d, want 1", len(runDiag.StartupFailures))
	}

	fail := runDiag.StartupFailures[0]
	if fail.Name != "assistant" {
		t.Errorf("failure name = %q, want assistant", fail.Name)
	}
	if strings.Contains(fail.Error, "123456789:ABCDEF") || strings.Contains(fail.Error, "secret123") {
		t.Errorf("sensitive credential leaked in StartupFailure error: %s", fail.Error)
	}
	if !strings.Contains(fail.Error, "[REDACTED_TOKEN]") || !strings.Contains(fail.Error, "token=[REDACTED]") {
		t.Errorf("expected redacted tokens in StartupFailure error: %s", fail.Error)
	}

	// Verify components in running state
	compMap := make(map[string]ComponentSnapshot)
	for _, c := range runDiag.Components {
		compMap[c.Name] = c
	}

	dbComp, ok := compMap["db"]
	if !ok || dbComp.State != "running" || dbComp.Status != "healthy" || dbComp.Error != "" {
		t.Errorf("unexpected db component: %+v", dbComp)
	}

	asstComp, ok := compMap["assistant"]
	if !ok || asstComp.State != "failed" || asstComp.Status != "degraded" {
		t.Errorf("unexpected assistant component: %+v", asstComp)
	}
	if strings.Contains(asstComp.Error, "123456789:ABCDEF") || !strings.Contains(asstComp.Error, "[REDACTED_TOKEN]") {
		t.Errorf("assistant component error credential leakage or missing redaction: %s", asstComp.Error)
	}

	// 3. Stop runtime
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("unexpected stop error: %v", err)
	}

	stopDiag := r.Diagnostics()
	if stopDiag.State != StateStopped {
		t.Fatalf("stopped diag state = %s, want %s", stopDiag.State, StateStopped)
	}
	stopCompMap := make(map[string]ComponentSnapshot)
	for _, c := range stopDiag.Components {
		stopCompMap[c.Name] = c
	}
	if stopCompMap["db"].State != "stopped" {
		t.Errorf("expected db component state 'stopped', got %q", stopCompMap["db"].State)
	}
}

func TestRuntime_DiagnosticsStartingState(t *testing.T) {
	r := New()

	comp1Started := make(chan struct{})
	proceedComp2 := make(chan struct{})

	comp1 := &callbackComponent{
		name: "comp1",
		startFn: func(ctx context.Context) error {
			return nil
		},
	}
	comp2 := &callbackComponent{
		name: "comp2",
		deps: []string{"comp1"},
		startFn: func(ctx context.Context) error {
			close(comp1Started)
			<-proceedComp2
			return nil
		},
	}

	if err := r.Register(comp1); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(comp2); err != nil {
		t.Fatal(err)
	}

	startDone := make(chan error, 1)
	go func() {
		startDone <- r.Start(context.Background())
	}()

	select {
	case <-comp1Started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for comp2 Start to be entered")
	}

	diag := r.Diagnostics()
	if diag.State != StateStarting {
		t.Fatalf("diagnostics state during startup = %q, want %q", diag.State, StateStarting)
	}

	compMap := make(map[string]ComponentSnapshot)
	for _, c := range diag.Components {
		compMap[c.Name] = c
	}

	c1, ok1 := compMap["comp1"]
	if !ok1 || c1.State != "starting" || c1.Status != "healthy" {
		t.Fatalf("started component comp1 during StateStarting = %+v, want state=starting, status=healthy", c1)
	}

	c2, ok2 := compMap["comp2"]
	if !ok2 || c2.State != "registered" || c2.Status != "" {
		t.Fatalf("in-progress/unstarted component comp2 during StateStarting = %+v, want state=registered, status=\"\"", c2)
	}

	close(proceedComp2)
	if err := <-startDone; err != nil {
		t.Fatalf("runtime start failed: %v", err)
	}

	runDiag := r.Diagnostics()
	if runDiag.State != StateRunning {
		t.Fatalf("state after startup = %q, want %q", runDiag.State, StateRunning)
	}
	for _, c := range runDiag.Components {
		if c.State != "running" || c.Status != "healthy" {
			t.Errorf("running component %q snapshot = %+v, want state=running, status=healthy", c.Name, c)
		}
	}

	_ = r.Stop(context.Background())
}

func TestRuntime_DiagnosticsStoppingState(t *testing.T) {
	r := New()

	stopEntered := make(chan struct{})
	proceedStop := make(chan struct{})

	comp := &callbackComponent{
		name: "stopping-comp",
		stopFn: func(ctx context.Context) error {
			close(stopEntered)
			<-proceedStop
			return nil
		},
	}
	if err := r.Register(comp); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- r.Stop(context.Background())
	}()

	select {
	case <-stopEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stopFn to be entered")
	}

	diag := r.Diagnostics()
	if diag.State != StateStopping {
		t.Fatalf("diagnostics state during stop = %q, want %q", diag.State, StateStopping)
	}
	if len(diag.Components) != 1 || diag.Components[0].State != "stopping" || diag.Components[0].Status != "healthy" {
		t.Fatalf("component snapshot during stopping = %+v, want state=stopping, status=healthy", diag.Components)
	}

	close(proceedStop)
	if err := <-stopDone; err != nil {
		t.Fatalf("stop failed: %v", err)
	}

	stoppedDiag := r.Diagnostics()
	if stoppedDiag.State != StateStopped {
		t.Fatalf("diagnostics state after stop = %q, want %q", stoppedDiag.State, StateStopped)
	}
	if len(stoppedDiag.Components) != 1 || stoppedDiag.Components[0].State != "stopped" || stoppedDiag.Components[0].Status != "healthy" {
		t.Fatalf("component snapshot after stop = %+v, want state=stopped, status=healthy", stoppedDiag.Components)
	}
}

func TestRuntime_DiagnosticsCriticalStartupFailure(t *testing.T) {
	r := New()

	comp1 := &mockComponent{name: "comp1"}
	comp2 := &mockComponent{
		name:         "comp2-critical",
		dependencies: []string{"comp1"},
		startErr:     errors.New("critical auth failed token=secret_token_12345"),
	}
	comp3 := &mockComponent{
		name:         "comp3-unreached",
		dependencies: []string{"comp2-critical"},
	}

	_ = r.Register(comp1)
	_ = r.Register(comp2)
	_ = r.Register(comp3)

	err := r.Start(context.Background())
	if err == nil {
		t.Fatal("expected critical startup failure, got nil")
	}
	if r.State() != StateFailed {
		t.Fatalf("runtime state = %q, want %q", r.State(), StateFailed)
	}

	diag := r.Diagnostics()
	if diag.State != StateFailed {
		t.Fatalf("diagnostics state = %q, want %q", diag.State, StateFailed)
	}

	// 1. Verify startup failure list
	if len(diag.StartupFailures) != 1 {
		t.Fatalf("expected 1 startup failure, got %d", len(diag.StartupFailures))
	}
	f := diag.StartupFailures[0]
	if f.Name != "comp2-critical" {
		t.Errorf("failure name = %q, want comp2-critical", f.Name)
	}
	if strings.Contains(f.Error, "secret_token_12345") || !strings.Contains(f.Error, "token=[REDACTED]") {
		t.Errorf("critical failure error credential leakage: %s", f.Error)
	}

	// 2. Verify component snapshots
	compMap := make(map[string]ComponentSnapshot)
	for _, c := range diag.Components {
		compMap[c.Name] = c
	}

	// comp1 was started and rolled back: runtime is failed -> comp1 state=failed, status=failed
	c1 := compMap["comp1"]
	if c1.State != "failed" || c1.Status != "failed" {
		t.Errorf("started component comp1 under failed runtime: %+v, want state=failed, status=failed", c1)
	}

	// comp2 failed startup: state=failed, status=failed (critical)
	c2 := compMap["comp2-critical"]
	if c2.State != "failed" || c2.Status != "failed" || !c2.Critical {
		t.Errorf("critical failing component: %+v, want state=failed, status=failed, critical=true", c2)
	}
	if strings.Contains(c2.Error, "secret_token_12345") || !strings.Contains(c2.Error, "token=[REDACTED]") {
		t.Errorf("credential leaked in component snapshot error: %s", c2.Error)
	}

	// comp3 was never reached: state=registered, status=""
	c3 := compMap["comp3-unreached"]
	if c3.State != "registered" || c3.Status != "" {
		t.Errorf("unreached component: %+v, want state=registered, status=\"\"", c3)
	}
}

type barrierComponent struct {
	name         string
	dependencies []string
	startFn      func(ctx context.Context) error
	stopFn       func(ctx context.Context) error
}

func (b *barrierComponent) Name() string           { return b.name }
func (b *barrierComponent) Dependencies() []string { return b.dependencies }
func (b *barrierComponent) Start(ctx context.Context) error {
	if b.startFn != nil {
		return b.startFn(ctx)
	}
	return nil
}
func (b *barrierComponent) Stop(ctx context.Context) error {
	if b.stopFn != nil {
		return b.stopFn(ctx)
	}
	return nil
}
func (b *barrierComponent) Health(ctx context.Context) ComponentHealth {
	return ComponentHealth{Status: HealthHealthy}
}

func TestRuntime_UptimeLifecycleSemantics(t *testing.T) {
	// 1. Created state: Uptime must be strictly 0
	r := New()
	if st := r.State(); st != StateCreated {
		t.Fatalf("expected StateCreated, got %v", st)
	}
	if u := r.Uptime(); u != 0 {
		t.Fatalf("uptime in StateCreated = %v, want 0", u)
	}

	// 2. Initializing & Starting state: Uptime must be strictly 0
	rInit := New()
	rInit.mu.Lock()
	if err := rInit.stateMachine.Transition(StateInitializing); err != nil {
		rInit.mu.Unlock()
		t.Fatalf("failed transition to StateInitializing: %v", err)
	}
	rInit.mu.Unlock()
	if st := rInit.State(); st != StateInitializing {
		t.Fatalf("expected StateInitializing, got %v", st)
	}
	if u := rInit.Uptime(); u != 0 {
		t.Fatalf("uptime in StateInitializing = %v, want 0", u)
	}

	rInit.mu.Lock()
	if err := rInit.stateMachine.Transition(StateStarting); err != nil {
		rInit.mu.Unlock()
		t.Fatalf("failed transition to StateStarting: %v", err)
	}
	rInit.mu.Unlock()
	if st := rInit.State(); st != StateStarting {
		t.Fatalf("expected StateStarting, got %v", st)
	}
	if u := rInit.Uptime(); u != 0 {
		t.Fatalf("uptime in StateStarting = %v, want 0", u)
	}

	// 3. Dynamic lifecycle transitions controlled via channel barriers:
	//    StateStarting -> StateRunning -> StateStopping -> StateStopped
	startEntered := make(chan struct{})
	startRelease := make(chan struct{})
	stopEntered := make(chan struct{})
	stopRelease := make(chan struct{})

	barrierComp := &barrierComponent{
		name: "barrier-comp",
		startFn: func(ctx context.Context) error {
			close(startEntered)
			<-startRelease
			return nil
		},
		stopFn: func(ctx context.Context) error {
			close(stopEntered)
			<-stopRelease
			return nil
		},
	}

	if err := r.Register(barrierComp); err != nil {
		t.Fatal(err)
	}

	startDone := make(chan error, 1)
	go func() {
		startDone <- r.Start(context.Background())
	}()

	// Wait until component Start is executing -> runtime is in StateStarting
	<-startEntered
	if st := r.State(); st != StateStarting {
		t.Fatalf("expected StateStarting during component start, got %v", st)
	}
	if u := r.Uptime(); u != 0 {
		t.Fatalf("uptime in StateStarting during active start = %v, want 0", u)
	}

	// Release Start barrier and wait for transition to StateRunning
	close(startRelease)
	if err := <-startDone; err != nil {
		t.Fatalf("Start() returned unexpected error: %v", err)
	}

	if st := r.State(); st != StateRunning {
		t.Fatalf("expected StateRunning, got %v", st)
	}

	// Running state: uptime must be > 0 and increasing without time.Sleep
	uRun1 := r.Uptime()
	var uRun2 time.Duration
	runDeadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(runDeadline) {
		uRun2 = r.Uptime()
		if uRun2 > uRun1 {
			break
		}
	}
	if uRun2 <= uRun1 {
		t.Fatalf("uptime in StateRunning did not increase: u1=%v, u2=%v", uRun1, uRun2)
	}

	// 4. Stopping state: uptime continues to be computed until shutdown completes
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- r.Stop(context.Background())
	}()

	// Wait until component Stop is executing -> runtime is in StateStopping
	<-stopEntered
	if st := r.State(); st != StateStopping {
		t.Fatalf("expected StateStopping during component stop, got %v", st)
	}
	uStopping1 := r.Uptime()
	if uStopping1 <= 0 || uStopping1 < uRun2 {
		t.Fatalf("uptime in StateStopping = %v, want >= %v", uStopping1, uRun2)
	}

	// Verify uptime continues to tick/advance during StateStopping
	var uStopping2 time.Duration
	stopTickDeadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(stopTickDeadline) {
		uStopping2 = r.Uptime()
		if uStopping2 > uStopping1 {
			break
		}
	}
	if uStopping2 <= uStopping1 {
		t.Fatalf("uptime in StateStopping did not increase: %v -> %v", uStopping1, uStopping2)
	}

	// Release Stop barrier and wait for transition to StateStopped
	close(stopRelease)
	if err := <-stopDone; err != nil {
		t.Fatalf("Stop() returned unexpected error: %v", err)
	}

	// 5. Stopped state: Uptime must freeze at stop time
	if st := r.State(); st != StateStopped {
		t.Fatalf("expected StateStopped, got %v", st)
	}
	stoppedUptime1 := r.Uptime()
	if stoppedUptime1 < uStopping2 {
		t.Fatalf("stopped uptime %v should be >= stopping uptime %v", stoppedUptime1, uStopping2)
	}

	freezeDeadline := time.Now().Add(10 * time.Millisecond)
	for time.Now().Before(freezeDeadline) {
		if u := r.Uptime(); u != stoppedUptime1 {
			t.Fatalf("uptime should be frozen after stop: got %v then %v", stoppedUptime1, u)
		}
	}

	// 6. Failed startup: Uptime must be strictly 0 (never ran)
	rFail := New()
	critFail := &mockComponent{name: "failing-comp", startErr: errors.New("boom")}
	_ = rFail.Register(critFail)
	_ = rFail.Start(context.Background())
	if rFail.State() != StateFailed {
		t.Fatalf("expected StateFailed, got %v", rFail.State())
	}
	if failUptime := rFail.Uptime(); failUptime != 0 {
		t.Fatalf("uptime for failed startup = %v, want 0", failUptime)
	}
}

func TestRuntime_LastShutdownReportDefensiveCopy(t *testing.T) {
	r := New()
	comp := &mockComponent{name: "test-comp"}
	_ = r.Register(comp)
	_ = r.Start(context.Background())
	_ = r.Stop(context.Background())

	rep1 := r.LastShutdownReport()
	if len(rep1.Phases) == 0 {
		t.Fatal("expected phases in shutdown report")
	}

	// Mutate returned slice
	rep1.Phases[0].Component = "tampered"
	rep1.Errors = append(rep1.Errors, errors.New("injected-error"))

	rep2 := r.LastShutdownReport()
	if rep2.Phases[0].Component == "tampered" {
		t.Fatal("LastShutdownReport exposed mutable Phases slice")
	}
	if len(rep2.Errors) == len(rep1.Errors) && len(rep1.Errors) > 0 {
		t.Fatal("LastShutdownReport exposed mutable Errors slice")
	}
}

func BenchmarkRuntime_Diagnostics(b *testing.B) {
	r := New()
	for i := 0; i < 10; i++ {
		_ = r.Register(&mockComponent{name: "comp-" + string(rune('a'+i))})
	}
	_ = r.Start(context.Background())
	defer func() { _ = r.Stop(context.Background()) }()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = r.Diagnostics()
	}
}
