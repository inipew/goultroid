package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// AggregateHealth represents the operational health status aggregated across all runtime components.
type AggregateHealth struct {
	Status     string            `json:"status"`
	Ready      bool              `json:"ready"`
	Runtime    State             `json:"runtime"`
	Reasons    []string          `json:"reasons,omitempty"`
	Components map[string]string `json:"components"`
}

// ShutdownPhase identifies the specific phase of graceful shutdown.
type ShutdownPhase string

const (
	PhaseQuiesce   ShutdownPhase = "quiesce"
	PhaseDrain     ShutdownPhase = "drain"
	PhaseStop      ShutdownPhase = "stop"
	PhaseForceStop ShutdownPhase = "force_stop"
)

// ShutdownPhaseReport captures the timing and outcome of a component in a shutdown phase.
type ShutdownPhaseReport struct {
	Phase     ShutdownPhase `json:"phase"`
	Component string        `json:"component"`
	Duration  time.Duration `json:"duration"`
	Error     error         `json:"error,omitempty"`
}

// ShutdownReport captures aggregated metrics from a runtime shutdown operation.
type ShutdownReport struct {
	TotalDuration time.Duration         `json:"total_duration"`
	Phases        []ShutdownPhaseReport `json:"phases"`
	Errors        []error               `json:"errors,omitempty"`
}

// Runtime is the sole owner and coordinator of the application lifecycle and component dependencies.
type Runtime struct {
	mu           sync.Mutex
	stateMachine *StateMachine
	graph        *DependencyGraph
	startedComps []Component
	failedComps  map[string]error

	rootCtx      context.Context
	rootCancel   context.CancelFunc
	opMu         sync.Mutex
	stopOnce     sync.Once
	stopDone     chan struct{}
	stopErr      error
	stopTimeout  time.Duration
	startTime    time.Time
	lastShutdown ShutdownReport
}

// New creates a new Runtime instance in the StateCreated state.
func New() *Runtime {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runtime{
		stateMachine: NewStateMachine(),
		graph:        NewDependencyGraph(),
		failedComps:  make(map[string]error),
		rootCtx:      ctx,
		rootCancel:   cancel,
		stopDone:     make(chan struct{}),
		stopTimeout:  30 * time.Second,
	}
}

// Context returns the root runtime context which is cancelled on shutdown.
func (r *Runtime) Context() context.Context {
	return r.rootCtx
}

// State returns the current lifecycle state of the runtime.
func (r *Runtime) State() State {
	return r.stateMachine.Current()
}

// Uptime returns the duration since the runtime reached StateRunning.
func (r *Runtime) Uptime() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.startTime.IsZero() {
		return 0
	}
	return time.Since(r.startTime)
}

// LastShutdownReport returns the metrics and phase breakdown from the most recent shutdown.
func (r *Runtime) LastShutdownReport() ShutdownReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastShutdown
}

// Register registers a component with the runtime. Components must be registered
// before Start is called.
func (r *Runtime) Register(c Component) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stateMachine.Current() != StateCreated {
		return fmt.Errorf("cannot register component %q when runtime is in state %s", c.Name(), r.stateMachine.Current())
	}

	return r.graph.Add(c)
}

// Component returns a registered component by name, or nil if not registered.
func (r *Runtime) Component(name string) Component {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, _ := r.graph.Get(name)
	return c
}

// Start validates the component dependency graph, deterministically starts components
// in topological order, and transitions the runtime to StateRunning.
// If any critical component fails to start, previously started components are rolled back
// in reverse order, and the runtime transitions to StateFailed.
func (r *Runtime) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.opMu.Lock()
	defer r.opMu.Unlock()

	r.mu.Lock()
	if err := r.stateMachine.Transition(StateInitializing); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("runtime start failed: %w", err)
	}

	order, err := r.graph.StartupOrder()
	if err != nil {
		r.stateMachine.SetFailed()
		r.mu.Unlock()
		return fmt.Errorf("dependency graph validation failed: %w", err)
	}

	if err := r.stateMachine.Transition(StateStarting); err != nil {
		r.stateMachine.SetFailed()
		r.mu.Unlock()
		return fmt.Errorf("runtime state transition failed: %w", err)
	}
	r.mu.Unlock()

	var started []Component
	failed := make(map[string]error)
	for _, comp := range order {
		if err := ctx.Err(); err != nil {
			return r.failStartAndRollback(fmt.Errorf("runtime startup cancelled: %w", err), started)
		}

		componentCtx, componentCancel := context.WithCancel(r.rootCtx)
		stopOperationCancel := context.AfterFunc(ctx, componentCancel)
		err := comp.Start(componentCtx)
		stopOperationCancel()
		if err != nil {
			componentCancel()
			if operationErr := ctx.Err(); operationErr != nil {
				return r.failStartAndRollback(fmt.Errorf("runtime startup cancelled: %w", operationErr), started)
			}
			isCritical := true
			if cc, ok := comp.(CriticalComponent); ok {
				isCritical = cc.IsCritical()
			}

			if isCritical {
				return r.failStartAndRollback(fmt.Errorf("component %q failed to start: %w", comp.Name(), err), started)
			}
			failed[comp.Name()] = err
		} else {
			started = append(started, comp)
		}
	}

	if err := ctx.Err(); err != nil {
		return r.failStartAndRollback(fmt.Errorf("runtime startup cancelled: %w", err), started)
	}

	r.mu.Lock()
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return r.failStartAndRollback(fmt.Errorf("runtime startup cancelled: %w", err), started)
	}
	r.startedComps = append([]Component(nil), started...)
	r.failedComps = failed
	if err := r.stateMachine.Transition(StateRunning); err != nil {
		r.stateMachine.SetFailed()
		r.mu.Unlock()
		return fmt.Errorf("failed to transition to running: %w", err)
	}

	r.startTime = time.Now()
	r.mu.Unlock()
	return nil
}

// StartupFailures returns a copy of components that failed to start during Start.
func (r *Runtime) StartupFailures() map[string]error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.failedComps) == 0 {
		return nil
	}
	res := make(map[string]error, len(r.failedComps))
	for k, v := range r.failedComps {
		res[k] = v
	}
	return res
}

func (r *Runtime) failStartAndRollback(startErr error, started []Component) error {
	r.stateMachine.SetFailed()
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	rollbackErrs := r.graph.Rollback(rollbackCtx, started)
	cancel()
	r.rootCancel()
	if len(rollbackErrs) == 0 {
		return startErr
	}
	causes := append([]error{startErr}, rollbackErrs...)
	return fmt.Errorf("runtime startup and rollback failed: %w", errors.Join(causes...))
}

func (r *Runtime) startStop(ownerCtx context.Context) {
	r.stopOnce.Do(func() {
		go func() {
			shutdownCtx, cancel := r.shutdownContext(ownerCtx)
			defer cancel()
			r.stopErr = r.performStop(shutdownCtx)
			close(r.stopDone)
		}()
	})
}

func (r *Runtime) shutdownContext(ownerCtx context.Context) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(r.stopTimeout)
	if ownerCtx != nil {
		if ownerDeadline, ok := ownerCtx.Deadline(); ok && ownerDeadline.Before(deadline) {
			deadline = ownerDeadline
		}
	}

	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	if ownerCtx == nil {
		return ctx, cancel
	}

	stopForward := context.AfterFunc(ownerCtx, cancel)
	if ownerCtx.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stopForward()
		cancel()
	}
}

// Stop gracefully stops all started components in reverse dependency order.
// Stop is idempotent; caller cancellation only bounds how long that caller
// waits. The actual teardown retains Runtime's own stop budget.
func (r *Runtime) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.startStop(nil)

	select {
	case <-r.stopDone:
		return r.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StopWithin is for the lifecycle owner. Unlike Stop, ctx is authoritative for
// the actual teardown as well as the caller wait, capped by Runtime's own stop
// timeout. This lets a composition root enforce one hard shutdown deadline.
func (r *Runtime) StopWithin(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.startStop(ctx)

	select {
	case <-r.stopDone:
		return r.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

const forcedStopReserve = 250 * time.Millisecond

func reserveForcedStopBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.Background(), func() {}
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return ctx, func() {}
	}
	cutoff := deadline.Add(-forcedStopReserve)
	if time.Until(cutoff) <= 0 {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, cutoff)
}

func (r *Runtime) performStop(ctx context.Context) error {
	r.opMu.Lock()
	defer r.opMu.Unlock()

	stopStart := time.Now()
	var report ShutdownReport

	recordPhase := func(phase ShutdownPhase, compName string, d time.Duration, err error) {
		report.Phases = append(report.Phases, ShutdownPhaseReport{
			Phase:     phase,
			Component: compName,
			Duration:  d,
			Error:     err,
		})
	}

	graceCtx, cancelGrace := reserveForcedStopBudget(ctx)
	defer cancelGrace()

	r.mu.Lock()
	if r.stateMachine.Current() == StateStopped {
		r.mu.Unlock()
		return nil
	}
	_ = r.stateMachine.Transition(StateStopping)
	var stopOrder []Component
	if len(r.startedComps) > 0 {
		n := len(r.startedComps)
		stopOrder = make([]Component, n)
		for i, c := range r.startedComps {
			stopOrder[n-1-i] = c
		}
	}
	r.mu.Unlock()

	var stopErrs []error
	graceExhausted := false
	for _, comp := range stopOrder {
		if err := graceCtx.Err(); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("graceful shutdown budget exhausted before component %q: %w", comp.Name(), err))
			graceExhausted = true
			break
		}
		if q, ok := comp.(Quiescer); ok {
			tStart := time.Now()
			err := q.Quiesce(graceCtx)
			recordPhase(PhaseQuiesce, comp.Name(), time.Since(tStart), err)
			if err != nil {
				stopErrs = append(stopErrs, fmt.Errorf("component %q quiesce failed: %w", comp.Name(), err))
			}
		}
		if err := graceCtx.Err(); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("graceful shutdown budget exhausted after quiescing %q: %w", comp.Name(), err))
			graceExhausted = true
			break
		}
		if d, ok := comp.(Drainer); ok {
			tStart := time.Now()
			err := d.Drain(graceCtx)
			recordPhase(PhaseDrain, comp.Name(), time.Since(tStart), err)
			if err != nil {
				stopErrs = append(stopErrs, fmt.Errorf("component %q drain failed: %w", comp.Name(), err))
			}
		}
		if graceCtx.Err() != nil {
			graceExhausted = true
			break
		}
	}

	forceMode := graceExhausted || graceCtx.Err() != nil
	for _, comp := range stopOrder {
		if !forceMode {
			tStart := time.Now()
			err := comp.Stop(graceCtx)
			recordPhase(PhaseStop, comp.Name(), time.Since(tStart), err)
			if err != nil {
				stopErrs = append(stopErrs, fmt.Errorf("component %q stop failed: %w", comp.Name(), err))
			}
			if graceCtx.Err() == nil {
				continue
			}
			forceMode = true
			stopErrs = append(stopErrs, fmt.Errorf("graceful shutdown budget exhausted while stopping %q: %w", comp.Name(), graceCtx.Err()))
		}
		if forced, ok := comp.(ForcedStopper); ok && ctx.Err() == nil {
			tStart := time.Now()
			err := forced.ForceStop(ctx)
			recordPhase(PhaseForceStop, comp.Name(), time.Since(tStart), err)
			if err != nil {
				stopErrs = append(stopErrs, fmt.Errorf("component %q forced stop failed: %w", comp.Name(), err))
			}
		}
	}

	r.rootCancel()
	if ctx.Err() != nil {
		stopErrs = append(stopErrs, fmt.Errorf("shutdown hard deadline exceeded: %w", ctx.Err()))
	}

	report.TotalDuration = time.Since(stopStart)
	report.Errors = append([]error(nil), stopErrs...)
	r.mu.Lock()
	r.lastShutdown = report
	r.mu.Unlock()

	if len(stopErrs) > 0 {
		r.stateMachine.SetFailed()
		return fmt.Errorf("shutdown completed with errors: %w", errors.Join(stopErrs...))
	}
	_ = r.stateMachine.Transition(StateStopped)
	return nil
}

// sanitizeHealthReason normalizes a detail or component string into a stable, bounded, machine-readable identifier.
// It strips punctuation, limits length to 48 chars, and prevents raw errors,
// tokens, paths, URLs, or payloads from entering health reasons.
func sanitizeHealthReason(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	if idx := strings.Index(s, "("); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}
	if idx := strings.Index(s, "?"); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}
	var b strings.Builder
	b.Grow(len(s))
	lastDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	res := strings.Trim(b.String(), "-")
	if len(res) > 48 {
		res = strings.TrimRight(res[:48], "-")
	}
	if res == "" {
		return "unknown"
	}
	return res
}

// Health probes all registered components and returns an aggregated health report.
// Health probes are read-only, context-aware, bounded, and preserve unknown/unavailable state.
func (r *Runtime) Health(ctx context.Context) AggregateHealth {
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	currentState := r.stateMachine.Current()
	components := append([]Component(nil), r.startedComps...)
	failedComps := make(map[string]error, len(r.failedComps))
	for k, v := range r.failedComps {
		failedComps[k] = v
	}
	r.mu.Unlock()

	report := AggregateHealth{
		Status:     HealthHealthy,
		Ready:      currentState == StateRunning,
		Runtime:    currentState,
		Components: make(map[string]string, len(components)+len(failedComps)),
	}

	if currentState != StateRunning {
		report.Ready = false
		report.Reasons = append(report.Reasons, fmt.Sprintf("runtime:state:%s", currentState))
		if currentState == StateFailed || currentState == StateStopped {
			report.Status = HealthUnhealthy
		} else {
			report.Status = HealthDegraded
		}
	}

	// If context is already cancelled/expired, record honest unknown/unavailable state
	// without dispatching probe calls.
	if err := ctx.Err(); err != nil {
		for _, comp := range components {
			compName := sanitizeHealthReason(comp.Name())
			report.Components[comp.Name()] = HealthUnknown
			report.Reasons = append(report.Reasons, fmt.Sprintf("%s:probe-cancelled", compName))
			isCritical := true
			if cc, ok := comp.(CriticalComponent); ok {
				isCritical = cc.IsCritical()
			}
			if isCritical {
				report.Ready = false
				if report.Status != HealthUnhealthy {
					report.Status = HealthDegraded
				}
			}
		}
		r.recordFailedComps(&report, failedComps)
		return report
	}

	n := len(components)
	type probeResult struct {
		idx    int
		health ComponentHealth
	}
	resultCh := make(chan probeResult, n)

	for i, comp := range components {
		go func(idx int, c Component) {
			defer func() {
				if rec := recover(); rec != nil {
					resultCh <- probeResult{
						idx: idx,
						health: ComponentHealth{
							Status:  HealthUnhealthy,
							Details: "panic",
						},
					}
				}
			}()

			h := c.Health(ctx)
			if h.Status == "" {
				h.Status = HealthHealthy
			}
			resultCh <- probeResult{idx: idx, health: h}
		}(i, comp)
	}

	completed := make(map[int]ComponentHealth, n)
collectLoop:
	for len(completed) < n {
		select {
		case res := <-resultCh:
			completed[res.idx] = res.health
		case <-ctx.Done():
			break collectLoop
		}
	}

	for i, comp := range components {
		compName := sanitizeHealthReason(comp.Name())
		isCritical := true
		if cc, ok := comp.(CriticalComponent); ok {
			isCritical = cc.IsCritical()
		}

		h, ok := completed[i]
		if !ok {
			h = ComponentHealth{
				Status:  HealthUnknown,
				Details: "probe-timeout",
			}
		}

		report.Components[compName] = h.Status

		switch h.Status {
		case HealthUnhealthy:
			if isCritical {
				report.Status = HealthUnhealthy
				report.Ready = false
			} else {
				if report.Status != HealthUnhealthy {
					report.Status = HealthDegraded
				}
			}
			if h.Details != "" {
				report.Reasons = append(report.Reasons, fmt.Sprintf("%s:%s", compName, sanitizeHealthReason(h.Details)))
			} else if h.Error != nil {
				reasonSuffix := "error"
				if errors.Is(h.Error, context.DeadlineExceeded) {
					reasonSuffix = "timeout"
				} else if errors.Is(h.Error, context.Canceled) {
					reasonSuffix = "cancelled"
				}
				report.Reasons = append(report.Reasons, fmt.Sprintf("%s:%s", compName, reasonSuffix))
			}
		case HealthUnknown:
			if isCritical {
				report.Ready = false
				if report.Status != HealthUnhealthy {
					report.Status = HealthDegraded
				}
			} else {
				if report.Status != HealthUnhealthy {
					report.Status = HealthDegraded
				}
			}
			if h.Details != "" {
				report.Reasons = append(report.Reasons, fmt.Sprintf("%s:%s", compName, sanitizeHealthReason(h.Details)))
			} else {
				report.Reasons = append(report.Reasons, fmt.Sprintf("%s:unknown", compName))
			}
		case HealthDegraded:
			if report.Status != HealthUnhealthy {
				report.Status = HealthDegraded
			}
			if h.Details != "" {
				report.Reasons = append(report.Reasons, fmt.Sprintf("%s:%s", compName, sanitizeHealthReason(h.Details)))
			}
		}
	}

	r.recordFailedComps(&report, failedComps)
	return report
}

func (r *Runtime) recordFailedComps(report *AggregateHealth, failedComps map[string]error) {
	if len(failedComps) == 0 {
		return
	}
	failedNames := make([]string, 0, len(failedComps))
	for name := range failedComps {
		failedNames = append(failedNames, name)
	}
	sort.Strings(failedNames)
	for _, name := range failedNames {
		report.Components[name] = HealthDegraded
		if report.Status != HealthUnhealthy {
			report.Status = HealthDegraded
		}
		report.Reasons = append(report.Reasons, fmt.Sprintf("runtime:optional-component-failed:%s", sanitizeHealthReason(name)))
	}
}
