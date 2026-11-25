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
