package runtime

import (
	"context"
	"errors"
	"fmt"
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

// Runtime is the sole owner and coordinator of the application lifecycle and component dependencies.
type Runtime struct {
	mu           sync.Mutex
	stateMachine *StateMachine
	graph        *DependencyGraph
	startedComps []Component

	rootCtx     context.Context
	rootCancel  context.CancelFunc
	opMu        sync.Mutex
	stopOnce    sync.Once
	stopDone    chan struct{}
	stopErr     error
	stopTimeout time.Duration
	startTime   time.Time
}

// New creates a new Runtime instance in the StateCreated state.
func New() *Runtime {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runtime{
		stateMachine: NewStateMachine(),
		graph:        NewDependencyGraph(),
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
				err = operationErr
			}
			isCritical := true
			if cc, ok := comp.(CriticalComponent); ok {
				isCritical = cc.IsCritical()
			}

			if isCritical {
				return r.failStartAndRollback(fmt.Errorf("component %q failed to start: %w", comp.Name(), err), started)
			}
		} else {
			started = append(started, comp)
		}
	}

	r.mu.Lock()
	r.startedComps = append([]Component(nil), started...)
	if err := r.stateMachine.Transition(StateRunning); err != nil {
		r.stateMachine.SetFailed()
		r.mu.Unlock()
		return fmt.Errorf("failed to transition to running: %w", err)
	}

	r.startTime = time.Now()
	r.mu.Unlock()
	return nil
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

// Stop gracefully stops all started components in reverse dependency order.
// Stop is idempotent; concurrent callers wait on the initial shutdown completion.
func (r *Runtime) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.stopOnce.Do(func() {
		go func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), r.stopTimeout)
			defer cancel()
			r.stopErr = r.performStop(shutdownCtx)
			close(r.stopDone)
		}()
	})

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
			if err := q.Quiesce(graceCtx); err != nil {
				stopErrs = append(stopErrs, fmt.Errorf("component %q quiesce failed: %w", comp.Name(), err))
			}
		}
		if err := graceCtx.Err(); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("graceful shutdown budget exhausted after quiescing %q: %w", comp.Name(), err))
			graceExhausted = true
			break
		}
		if d, ok := comp.(Drainer); ok {
			if err := d.Drain(graceCtx); err != nil {
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
			if err := comp.Stop(graceCtx); err != nil {
				stopErrs = append(stopErrs, fmt.Errorf("component %q stop failed: %w", comp.Name(), err))
			}
			if graceCtx.Err() == nil {
				continue
			}
			forceMode = true
			stopErrs = append(stopErrs, fmt.Errorf("graceful shutdown budget exhausted while stopping %q: %w", comp.Name(), graceCtx.Err()))
		}
		if forced, ok := comp.(ForcedStopper); ok && ctx.Err() == nil {
			if err := forced.ForceStop(ctx); err != nil {
				stopErrs = append(stopErrs, fmt.Errorf("component %q forced stop failed: %w", comp.Name(), err))
			}
		}
	}

	r.rootCancel()
	if ctx.Err() != nil {
		stopErrs = append(stopErrs, fmt.Errorf("shutdown hard deadline exceeded: %w", ctx.Err()))
	}
	if len(stopErrs) > 0 {
		r.stateMachine.SetFailed()
		return fmt.Errorf("shutdown completed with errors: %w", errors.Join(stopErrs...))
	}
	_ = r.stateMachine.Transition(StateStopped)
	return nil
}

// Health probes all registered components and returns an aggregated health report.
func (r *Runtime) Health(ctx context.Context) AggregateHealth {
	r.mu.Lock()
	currentState := r.stateMachine.Current()
	components := append([]Component(nil), r.startedComps...)
	r.mu.Unlock()

	report := AggregateHealth{
		Status:     HealthHealthy,
		Ready:      currentState == StateRunning,
		Runtime:    currentState,
		Components: make(map[string]string),
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

	for _, comp := range components {
		h := comp.Health(ctx)
		status := h.Status
		if status == "" {
			status = HealthHealthy
		}
		report.Components[comp.Name()] = status

		if status == HealthUnhealthy {
			report.Status = HealthUnhealthy
			if h.Details != "" {
				report.Reasons = append(report.Reasons, fmt.Sprintf("%s:%s", comp.Name(), h.Details))
			} else if h.Error != nil {
				report.Reasons = append(report.Reasons, fmt.Sprintf("%s:%s", comp.Name(), h.Error.Error()))
			}
		} else if status == HealthDegraded && report.Status != HealthUnhealthy {
			report.Status = HealthDegraded
			if h.Details != "" {
				report.Reasons = append(report.Reasons, fmt.Sprintf("%s:%s", comp.Name(), h.Details))
			}
		}
	}

	return report
}
