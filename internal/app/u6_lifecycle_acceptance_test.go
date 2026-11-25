package app

import (
	"context"
	"errors"
	"fmt"
	stdruntime "runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

type u6LifecycleTestComp struct {
	name      string
	deps      []string
	critical  bool
	startErr  error
	stopErr   error
	forceErr  error
	ignoreCtx bool

	eventsMu *sync.Mutex
	events   *[]string

	startGate *u6ExecutionGate
	stopGate  *u6ExecutionGate
	forceGate *u6ExecutionGate
}

func (c *u6LifecycleTestComp) Name() string           { return c.name }
func (c *u6LifecycleTestComp) Dependencies() []string { return c.deps }
func (c *u6LifecycleTestComp) IsCritical() bool       { return c.critical }

func (c *u6LifecycleTestComp) record(ev string) {
	if c.events == nil {
		return
	}
	if c.eventsMu != nil {
		c.eventsMu.Lock()
		defer c.eventsMu.Unlock()
	}
	*c.events = append(*c.events, ev)
}

func (c *u6LifecycleTestComp) Start(ctx context.Context) error {
	c.record("start:" + c.name)
	if c.startGate != nil {
		c.startGate.MarkStarted()
		_ = c.startGate.WaitRelease(ctx)
	}
	return c.startErr
}

func (c *u6LifecycleTestComp) Quiesce(ctx context.Context) error {
	c.record("quiesce:" + c.name)
	return nil
}

func (c *u6LifecycleTestComp) Drain(ctx context.Context) error {
	c.record("drain:" + c.name)
	return nil
}

func (c *u6LifecycleTestComp) Stop(ctx context.Context) error {
	c.record("stop:" + c.name)
	if c.stopGate != nil {
		c.stopGate.MarkStarted()
		if c.ignoreCtx {
			<-c.stopGate.releaseCh
		} else {
			if err := c.stopGate.WaitRelease(ctx); err != nil {
				return err
			}
		}
	}
	return c.stopErr
}

func (c *u6LifecycleTestComp) ForceStop(ctx context.Context) error {
	c.record("force_stop:" + c.name)
	if c.forceGate != nil {
		c.forceGate.MarkStarted()
		if err := c.forceGate.WaitRelease(ctx); err != nil {
			return err
		}
	}
	return c.forceErr
}

func (c *u6LifecycleTestComp) Health(context.Context) runtime.ComponentHealth {
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}

type u6PanicReporterSpy struct {
	mu      sync.Mutex
	reports []runtime.PanicReport
	notify  chan struct{}
}

func newU6PanicReporterSpy() *u6PanicReporterSpy {
	return &u6PanicReporterSpy{notify: make(chan struct{}, 16)}
}

func (p *u6PanicReporterSpy) ReportPanic(r runtime.PanicReport) {
	p.mu.Lock()
	p.reports = append(p.reports, r)
	p.mu.Unlock()
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

func (p *u6PanicReporterSpy) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reports)
}

func TestU6_S01_StartupCriticalFailureRollback(t *testing.T) {
	baselineG := stdruntime.NumGoroutine()

	rt := runtime.New()
	var events []string
	var eventsMu sync.Mutex

	compA := &u6LifecycleTestComp{name: "db-like", critical: true}
	compB := &u6LifecycleTestComp{name: "bus-like", deps: []string{"db-like"}, critical: true}
	compFail := &u6LifecycleTestComp{
		name:     "critical-fail",
		deps:     []string{"bus-like"},
		critical: true,
		startErr: errors.New("critical init failed"),
	}
	for _, c := range []*u6LifecycleTestComp{compA, compB, compFail} {
		c.eventsMu = &eventsMu
		c.events = &events
		if err := rt.Register(c); err != nil {
			t.Fatalf("Register(%s): %v", c.name, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := rt.Start(ctx)
	if err == nil {
		t.Fatal("expected rt.Start to fail when critical component fails")
	}
	if rt.State() != runtime.StateFailed {
		t.Fatalf("runtime state = %s, want %s", rt.State(), runtime.StateFailed)
	}
	select {
	case <-rt.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("expected runtime root context to be canceled after rollback")
	}

	// Verify reverse topological rollback order: bus-like stopped before db-like.
	stopBIdx := slices.Index(events, "stop:bus-like")
	stopAIdx := slices.Index(events, "stop:db-like")
	if stopBIdx == -1 || stopAIdx == -1 || stopBIdx > stopAIdx {
		t.Fatalf("expected reverse rollback stop:bus-like before stop:db-like, got events=%v", events)
	}
	if slices.Contains(events, "stop:critical-fail") {
		t.Fatalf("failed component should not be stopped, got events=%v", events)
	}

	waitForGoroutinesSettled(t, baselineG, 3, 2*time.Second)
}

func TestU6_S02_StartupNonCriticalFailureDegradesGracefully(t *testing.T) {
	rt := runtime.New()
	engine := taskengine.NewEngine(taskengine.NewDefaultConfig())

	nonCrit := &u6LifecycleTestComp{
		name:     "optional-addon",
		critical: false,
		startErr: errors.New("addon unavailable"),
	}
	if err := rt.Register(engine); err != nil {
		t.Fatalf("Register(taskengine): %v", err)
	}
	if err := rt.Register(nonCrit); err != nil {
		t.Fatalf("Register(optional-addon): %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rt.Start(ctx); err != nil {
		t.Fatalf("expected non-critical component failure not to fail runtime startup: %v", err)
	}
	if rt.State() != runtime.StateRunning {
		t.Fatalf("runtime state = %s, want %s", rt.State(), runtime.StateRunning)
	}

	diag := rt.Diagnostics()
	var foundDegraded bool
	for _, snap := range diag.Components {
		if snap.Name == "optional-addon" {
			if snap.Status != "degraded" {
				t.Fatalf("optional-addon status = %q, want degraded", snap.Status)
			}
			foundDegraded = true
		}
	}
	if !foundDegraded {
		t.Fatalf("optional-addon missing from diagnostics: %+v", diag.Components)
	}

	// Verify core workload continues operating normally.
	ticket, err := engine.Submit(ctx, tasks.WorkSpec{
		ID:         "s02-core-work",
		QuotaOwner: "test-owner",
		Pool:       "general",
		Handler: func(context.Context) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("core TaskEngine Submit failed while optional component degraded: %v", err)
	}
	res, err := ticket.Wait(ctx)
	if err != nil || res.Outcome != tasks.OutcomeCompleted {
		t.Fatalf("core work failed: res=%+v err=%v", res, err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := rt.StopWithin(stopCtx); err != nil {
		t.Fatalf("rt.StopWithin: %v", err)
	}
}

type u6QuiesceSignalEngine struct {
	*taskengine.Engine
	quiescedOnce sync.Once
	quiescedCh   chan struct{}
}

func (w *u6QuiesceSignalEngine) Quiesce(ctx context.Context) error {
	err := w.Engine.Quiesce(ctx)
	w.quiescedOnce.Do(func() { close(w.quiescedCh) })
	return err
}

func TestU6_S09_GracefulShutdownPhaseOrdering(t *testing.T) {
	rt := runtime.New()
	engine := taskengine.NewEngine(taskengine.NewDefaultConfig())
	wrappedEngine := &u6QuiesceSignalEngine{
		Engine:     engine,
		quiescedCh: make(chan struct{}),
	}

	var eventsMu sync.Mutex
	var events []string

	compBase := &u6LifecycleTestComp{name: "base", critical: true, eventsMu: &eventsMu, events: &events}
	compTop := &u6LifecycleTestComp{name: "top", deps: []string{"base"}, critical: true, eventsMu: &eventsMu, events: &events}

	if err := rt.Register(wrappedEngine); err != nil {
		t.Fatalf("Register(engine): %v", err)
	}
	if err := rt.Register(compBase); err != nil {
		t.Fatalf("Register(base): %v", err)
	}
	if err := rt.Register(compTop); err != nil {
		t.Fatalf("Register(top): %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}

	// Hold an in-flight TaskEngine job on a deterministic gate.
	gate := newU6ExecutionGate()
	ticket, err := engine.Submit(ctx, tasks.WorkSpec{
		ID:         "s09-inflight",
		QuotaOwner: "test-owner",
		Pool:       "general",
		Handler: func(runCtx context.Context) error {
			defer gate.MarkDone()
			gate.MarkStarted()
			return gate.WaitRelease(runCtx)
		},
	})
	if err != nil {
		t.Fatalf("Submit in-flight task: %v", err)
	}
	if err := gate.WaitStarted(ctx); err != nil {
		t.Fatalf("WaitStarted: %v", err)
	}

	stopErrCh := make(chan error, 1)
	go func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		stopErrCh <- rt.StopWithin(stopCtx)
	}()

	// Wait until runtime enters StateStopping before asserting admission rejection.
	reachedStopping := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rt.State() == runtime.StateStopping {
			reachedStopping = true
			break
		}
		stdruntime.Gosched()
	}
	if !reachedStopping {
		gate.Release()
		t.Fatal("runtime never reached StateStopping before deadline")
	}

	// Wait deterministically for TaskEngine.Quiesce to complete before asserting admission rejection.
	u6WaitClosed(t, wrappedEngine.quiescedCh, 2*time.Second, "taskengine Quiesce")

	_, rejectErr := engine.Submit(ctx, tasks.WorkSpec{
		ID:         "s09-rejected-during-quiesce",
		QuotaOwner: "test-owner",
		Pool:       "general",
		Handler:    func(context.Context) error { return nil },
	})
	if !errors.Is(rejectErr, tasks.ErrEngineQuiescing) {
		gate.Release()
		t.Fatalf("expected ErrEngineQuiescing during shutdown, got: %v", rejectErr)
	}

	// Release in-flight task so Drain completes cleanly.
	gate.Release()
	if err := gate.WaitDone(ctx); err != nil {
		t.Fatalf("WaitDone: %v", err)
	}
	if _, err := ticket.Wait(ctx); err != nil {
		t.Fatalf("ticket.Wait: %v", err)
	}

	select {
	case err := <-stopErrCh:
		if err != nil {
			t.Fatalf("rt.StopWithin failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("rt.StopWithin timed out")
	}

	if rt.State() != runtime.StateStopped {
		t.Fatalf("runtime state = %s, want %s", rt.State(), runtime.StateStopped)
	}

	// Verify reverse dependency phase order: quiesce:top -> drain:top -> quiesce:base -> drain:base -> stop:top -> stop:base.
	expectedOrder := []string{"quiesce:top", "drain:top", "quiesce:base", "drain:base", "stop:top", "stop:base"}
	var filtered []string
	for _, ev := range events {
		if !strings.HasPrefix(ev, "start:") {
			filtered = append(filtered, ev)
		}
	}
	if !slices.Equal(filtered, expectedOrder) {
		t.Fatalf("shutdown phase order = %v, want %v", filtered, expectedOrder)
	}
}

func TestU6_S10A_ForcedStopWithRemainingCleanupBudget(t *testing.T) {
	rt := runtime.New()
	resMgr := resource.NewManager()

	stopGate := newU6ExecutionGate()
	forceCalled := atomic.Bool{}
	comp := &u6LifecycleTestComp{
		name:     "slow-stop-comp",
		critical: true,
		stopGate: stopGate,
		forceErr: errors.New("forced stop completed with residual error"),
	}
	if err := rt.Register(comp); err != nil {
		t.Fatalf("rt.Register: %v", err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}

	cleaned := atomic.Bool{}
	if err := resMgr.RegisterWithCleanup(resource.Resource{
		ID:    "s10a-res",
		Owner: "test-owner",
		Type:  resource.TypeTempFile,
	}, func() error {
		cleaned.Store(true)
		return nil
	}); err != nil {
		t.Fatalf("RegisterWithCleanup: %v", err)
	}

	// Single parent context as the hard shutdown deadline. Runtime.StopWithin uses
	// a derived 400ms sub-budget (grace ~150ms + ForceStop reserve 250ms), and
	// ForceCleanupAllContext reuses the remaining budget of the same parent context.
	sharedCtx, sharedCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer sharedCancel()

	rtCtx, rtCancel := context.WithTimeout(sharedCtx, 400*time.Millisecond)
	defer rtCancel()
	stopErr := rt.StopWithin(rtCtx)
	if stopErr == nil {
		t.Fatal("expected Runtime.StopWithin to return forced-stop error")
	}

	report := rt.LastShutdownReport()
	for _, p := range report.Phases {
		if p.Phase == runtime.PhaseForceStop && p.Component == "slow-stop-comp" {
			forceCalled.Store(true)
		}
	}
	if !forceCalled.Load() {
		t.Fatalf("expected PhaseForceStop in ShutdownReport, got %+v", report.Phases)
	}
	if rt.State() != runtime.StateFailed {
		t.Fatalf("runtime state = %s, want %s", rt.State(), runtime.StateFailed)
	}

	// Remaining budget of the same parent sharedCtx still allows ForceCleanupAllContext to clean resources.
	if errs := resMgr.ForceCleanupAllContext(sharedCtx); len(errs) > 0 {
		t.Fatalf("expected ForceCleanupAllContext to succeed with remaining parent budget, got: %v", errs)
	}
	if !cleaned.Load() {
		t.Fatal("expected tracked resource cleanup callback to run")
	}
	if err := resMgr.VerifyShutdownClean(); err != nil {
		t.Fatalf("VerifyShutdownClean: %v", err)
	}
}

func TestU6_S10B_ForcedStopHardDeadlineExhausted(t *testing.T) {
	baselineG := stdruntime.NumGoroutine()

	rt := runtime.New()
	resMgr := resource.NewManager()
	sup := runtime.NewSupervisor(runtime.WithMaxWorkers(4))

	workerGate := newU6ExecutionGate()
	if err := sup.Register(runtime.WorkerSpec{
		Name:    "recalcitrant-worker",
		Restart: runtime.NeverRestart,
		Run: func(ctx context.Context) error {
			defer workerGate.MarkDone()
			workerGate.MarkStarted()
			// Intentionally ignore ctx.Done() until workerGate is released.
			<-workerGate.releaseCh
			return nil
		},
	}); err != nil {
		t.Fatalf("sup.Register: %v", err)
	}
	if err := rt.Register(&supervisorComp{name: "supervisor", supervisor: sup}); err != nil {
		t.Fatalf("rt.Register: %v", err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}
	if err := workerGate.WaitStarted(context.Background()); err != nil {
		t.Fatalf("WaitStarted: %v", err)
	}

	cleaned := atomic.Bool{}
	if err := resMgr.RegisterWithCleanup(resource.Resource{
		ID:    "s10b-leaked-res",
		Owner: "test-owner",
		Type:  resource.TypeTempFile,
	}, func() error {
		cleaned.Store(true)
		return nil
	}); err != nil {
		t.Fatalf("RegisterWithCleanup: %v", err)
	}

	// Shared shutdown deadline exhausted by recalcitrant worker.
	sharedCtx, sharedCancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer sharedCancel()

	stopErr := rt.StopWithin(sharedCtx)
	if stopErr == nil {
		t.Fatal("expected StopWithin to fail when recalcitrant worker exhausts hard deadline")
	}
	if sup.State() == runtime.StateStopped {
		t.Fatalf("supervisor falsely claimed StateStopped while worker is active")
	}
	// Runtime must NOT be StateStopped (either StateStopping while ForceStop unwinds or StateFailed).
	if rt.State() == runtime.StateStopped {
		t.Fatalf("runtime falsely claimed StateStopped while worker is active")
	}

	// Exhausted sharedCtx causes ForceCleanupAllContext to abort immediately without running callbacks.
	cleanupErrs := resMgr.ForceCleanupAllContext(sharedCtx)
	if len(cleanupErrs) == 0 || !errors.Is(cleanupErrs[0], context.DeadlineExceeded) {
		t.Fatalf("expected ForceCleanupAllContext to return DeadlineExceeded on exhausted ctx, got: %v", cleanupErrs)
	}
	if cleaned.Load() {
		t.Fatal("cleanup callback must not run after hard deadline is exhausted")
	}
	if leakErr := resMgr.VerifyShutdownClean(); leakErr == nil || !strings.Contains(leakErr.Error(), "test-owner:[temp_file=1]") {
		t.Fatalf("expected truthful leak report from VerifyShutdownClean, got: %v", leakErr)
	}

	// Release worker barrier so test cleans up goroutines cleanly.
	workerGate.Release()
	if err := workerGate.WaitDone(context.Background()); err != nil {
		t.Fatalf("WaitDone: %v", err)
	}
	waitForGoroutinesSettled(t, baselineG, 3, 2*time.Second)
}

func TestU6_S11_RepeatedAppLifecycleNoLeakOrGrowth(t *testing.T) {
	// Warmup round so one-time runtime/driver initialization does not skew baseline.
	{
		app, err := New(u6TestAppConfig(t))
		if err != nil {
			t.Fatalf("warmup New: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := app.startBackgroundServices(ctx); err != nil {
			cancel()
			t.Fatalf("warmup startBackgroundServices: %v", err)
		}
		cancel()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := app.Shutdown(shutCtx); err != nil {
			shutCancel()
			t.Fatalf("warmup Shutdown: %v", err)
		}
		shutCancel()
	}

	baselineG := stdruntime.NumGoroutine()
	const rounds = 5

	for round := 0; round < rounds; round++ {
		app, err := New(u6TestAppConfig(t))
		if err != nil {
			t.Fatalf("round %d New: %v", round, err)
		}

		startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := app.startBackgroundServices(startCtx); err != nil {
			startCancel()
			t.Fatalf("round %d startBackgroundServices: %v", round, err)
		}
		startCancel()

		baseWorkers := len(app.supervisor.Workers())
		// Exercise Supervisor worker with explicit done tracking.
		workerGate := newU6ExecutionGate()
		if err := app.supervisor.Register(runtime.WorkerSpec{
			Name:    fmt.Sprintf("s11-worker-%d", round),
			Restart: runtime.NeverRestart,
			Run: func(ctx context.Context) error {
				defer workerGate.MarkDone()
				workerGate.MarkStarted()
				<-ctx.Done()
				return nil
			},
		}); err != nil {
			t.Fatalf("round %d supervisor.Register: %v", round, err)
		}
		if err := workerGate.WaitStarted(context.Background()); err != nil {
			t.Fatalf("round %d worker WaitStarted: %v", round, err)
		}
		if snaps := app.supervisor.Workers(); len(snaps) != baseWorkers+1 {
			t.Fatalf("round %d supervisor workers = %d, want %d (no duplicate/stale workers)", round, len(snaps), baseWorkers+1)
		}

		// Exercise TaskEngine finite work.
		ticket, err := app.taskEngine.Submit(context.Background(), tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("s11-task-%d", round)),
			QuotaOwner: "s11-owner",
			Pool:       "general",
			Handler:    func(context.Context) error { return nil },
		})
		if err != nil {
			t.Fatalf("round %d TaskEngine Submit: %v", round, err)
		}
		res, err := ticket.Wait(context.Background())
		if err != nil || res.Outcome != tasks.OutcomeCompleted {
			t.Fatalf("round %d task failed: res=%+v err=%v", round, res, err)
		}

		// Exercise ResourceManager cleanup.
		cleaned := atomic.Bool{}
		if err := app.resources.RegisterWithCleanup(resource.Resource{
			ID:    fmt.Sprintf("s11-res-%d", round),
			Owner: "s11-plugin",
			Type:  resource.TypeTempFile,
		}, func() error {
			cleaned.Store(true)
			return nil
		}); err != nil {
			t.Fatalf("round %d RegisterWithCleanup: %v", round, err)
		}

		shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := app.Shutdown(shutCtx); err != nil {
			shutCancel()
			t.Fatalf("round %d Shutdown: %v", round, err)
		}
		shutCancel()

		if err := workerGate.WaitDone(context.Background()); err != nil {
			t.Fatalf("round %d worker WaitDone: %v", round, err)
		}
		if !cleaned.Load() {
			t.Fatalf("round %d resource was not cleaned", round)
		}
		if err := app.resources.VerifyShutdownClean(); err != nil {
			t.Fatalf("round %d VerifyShutdownClean: %v", round, err)
		}
		if app.LifecycleState() != "stopped" {
			t.Fatalf("round %d lifecycle state = %q, want stopped", round, app.LifecycleState())
		}
	}

	// Fixed tolerance (3) after warmup: a 1-goroutine-per-round leak across 5 rounds (+5) will fail.
	waitForGoroutinesSettled(t, baselineG, 3, 3*time.Second)
}

func TestU6_S13_SupervisorCrashLoopIsolationAndHealth(t *testing.T) {
	reporter := newU6PanicReporterSpy()
	sup := runtime.NewSupervisor(
		runtime.WithMaxWorkers(4),
		runtime.WithBaseBackoff(5*time.Millisecond),
		runtime.WithPanicReporter(reporter),
	)
	rt := runtime.New()

	var runs atomic.Int32
	if err := sup.Register(runtime.WorkerSpec{
		Name:          "crash-loop-worker",
		Restart:       runtime.RestartTransient,
		MaxRestarts:   2,
		RestartWindow: 10 * time.Second,
		Run: func(ctx context.Context) error {
			runs.Add(1)
			panic("boom-worker-panic")
		},
	}); err != nil {
		t.Fatalf("sup.Register: %v", err)
	}
	if err := rt.Register(&supervisorComp{name: "supervisor", supervisor: sup}); err != nil {
		t.Fatalf("rt.Register: %v", err)
	}

	a := &App{
		runtime:      rt,
		resources:    resource.NewManager(),
		shutdownDone: make(chan struct{}),
	}
	a.lifecycleState.Store(uint32(lifecycleRunning))

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}

	// Wait deterministically until worker reaches WorkerStateDegraded after exhausting MaxRestarts=2 (3 total runs).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snaps := sup.Workers()
		if len(snaps) == 1 && snaps[0].State == runtime.WorkerStateDegraded {
			break
		}
		stdruntime.Gosched()
	}

	snaps := sup.Workers()
	if len(snaps) != 1 || snaps[0].State != runtime.WorkerStateDegraded {
		t.Fatalf("expected crash-loop worker in WorkerStateDegraded, got %+v", snaps)
	}
	if got := runs.Load(); got != 3 {
		t.Fatalf("expected 3 total runs (initial + 2 restarts), got %d", got)
	}
	if got := reporter.Count(); got != 3 {
		t.Fatalf("expected 3 panic reports, got %d", got)
	}

	// App remains running and reports degraded health without crashing.
	health := a.HealthContext(context.Background())
	if health.Status != HealthDegraded {
		t.Fatalf("expected App health degraded during supervisor crash loop, got %q (%v)", health.Status, health.Reasons)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := a.Shutdown(stopCtx); err != nil {
		t.Fatalf("App.Shutdown: %v", err)
	}
}
