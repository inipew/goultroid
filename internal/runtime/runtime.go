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

	rootCtx    context.Context
	rootCancel context.CancelFunc
	opMu       sync.Mutex
	stopOnce   sync.Once
	stopDone   chan struct{}
	stopErr    error
	startTime  time.Time
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
	r.stopOnce.Do(func() {
		r.stopErr = r.performStop(ctx)
		close(r.stopDone)
	})

	select {
	case <-r.stopDone:
		return r.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) performStop(ctx context.Context) error {
	r.opMu.Lock()
	defer r.opMu.Unlock()

	r.mu.Lock()
	currentState := r.stateMachine.Current()
	if currentState == StateStopped {
		r.mu.Unlock()
		return nil
	}

	_ = r.stateMachine.Transition(StateStopping)

	// Determine shutdown order
	var stopOrder []Component
	if len(r.startedComps) > 0 {
		// Stop only what was started, in reverse order
		n := len(r.startedComps)
		stopOrder = make([]Component, n)
		for i, c := range r.startedComps {
			stopOrder[n-1-i] = c
		}
	}
	r.mu.Unlock()

	var stopErrs []error
	for _, phase := range []struct {
		name string
		run  func(Component) error
	}{
		{name: "quiesce", run: func(comp Component) error {
			if q, ok := comp.(Quiescer); ok {
				return q.Quiesce(ctx)
			}
			return nil
		}},
		{name: "drain", run: func(comp Component) error {
			if d, ok := comp.(Drainer); ok {
				return d.Drain(ctx)
			}
			return nil
		}},
	} {
		for _, comp := range stopOrder {
			if err := ctx.Err(); err != nil {
				stopErrs = append(stopErrs, fmt.Errorf("%s deadline exceeded before component %q: %w", phase.name, comp.Name(), err))
				break
			}
			if err := phase.run(comp); err != nil {
				stopErrs = append(stopErrs, fmt.Errorf("component %q %s failed: %w", comp.Name(), phase.name, err))
			}
		}
	}
	for _, comp := range stopOrder {
		if err := ctx.Err(); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("stop deadline exceeded before stopping %q: %w", comp.Name(), err))
			break
		}

		if err := comp.Stop(ctx); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("component %q stop failed: %w", comp.Name(), err))
		}
	}

	// Cancel root context after components have stopped or attempted to stop
	r.rootCancel()
	_ = r.stateMachine.Transition(StateStopped)

	if len(stopErrs) > 0 {
		return fmt.Errorf("shutdown completed with errors: %w", errors.Join(stopErrs...))
	}
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
