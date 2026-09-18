package runtime

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"runtime/debug"
	"sync"
	"time"
)

// PanicReport contains structured details about a recovered panic.
type PanicReport struct {
	Owner     string
	Component string
	Value     any
	Stack     []byte
	At        time.Time
}

// PanicReporter accepts structured reports of recovered panics.
type PanicReporter interface {
	ReportPanic(PanicReport)
}

// RestartPolicy determines whether and when a worker should be restarted after exit.
type RestartPolicy uint8

const (
	// NeverRestart indicates the worker should not be restarted under any condition.
	NeverRestart RestartPolicy = iota
	// RestartTransient restarts the worker only if it exits with an error or panic.
	RestartTransient
)

// WorkerSpec defines the specification for a managed supervisor worker.
type WorkerSpec struct {
	Name          string
	Restart       RestartPolicy
	MaxRestarts   int
	RestartWindow time.Duration
	Run           func(context.Context) error
}

// WorkerState describes the operational state of a supervisor worker.
type WorkerState string

const (
	WorkerStateCreated  WorkerState = "created"
	WorkerStateRunning  WorkerState = "running"
	WorkerStateStopped  WorkerState = "stopped"
	WorkerStateFailed   WorkerState = "failed"
	WorkerStateDegraded WorkerState = "degraded"
)

// WorkerSnapshot provides an immutable view of a worker's runtime state.
type WorkerSnapshot struct {
	Name         string        `json:"name"`
	State        WorkerState   `json:"state"`
	RestartCount int           `json:"restart_count"`
	LastError    string        `json:"last_error,omitempty"`
	Panics       int           `json:"panics"`
	Uptime       time.Duration `json:"uptime"`
}

const (
	DefaultSupervisorName = "supervisor"
	DefaultMaxWorkers     = 64
	DefaultBaseBackoff    = 25 * time.Millisecond
	DefaultRestartWindow  = 10 * time.Second
	DefaultMaxRestarts    = 3
)

// Supervisor coordinates lifecycle-owned workers, enforcing panic recovery,
// exponential backoff with jitter, restart budgets, and bounded shutdown.
// It implements runtime.Component and runtime.Quiescer.
type Supervisor struct {
	name        string
	maxWorkers  int
	reporter    PanicReporter
	baseBackoff time.Duration

	mu       sync.RWMutex
	state    State
	workers  map[string]*workerEntry
	order    []string
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	quiesced bool
	stopOnce sync.Once
	stopDone chan struct{}
}

// SupervisorOption configures a Supervisor.
type SupervisorOption func(*Supervisor)

// WithSupervisorName sets the component name for the supervisor.
func WithSupervisorName(name string) SupervisorOption {
	return func(s *Supervisor) {
		if name != "" {
			s.name = name
		}
	}
}

// WithMaxWorkers sets the maximum number of concurrent workers allowed.
func WithMaxWorkers(max int) SupervisorOption {
	return func(s *Supervisor) {
		if max > 0 {
			s.maxWorkers = max
		}
	}
}

// WithPanicReporter sets the panic reporter for recovered worker panics.
func WithPanicReporter(reporter PanicReporter) SupervisorOption {
	return func(s *Supervisor) {
		s.reporter = reporter
	}
}

// WithBaseBackoff sets the base restart backoff duration.
func WithBaseBackoff(backoff time.Duration) SupervisorOption {
	return func(s *Supervisor) {
		if backoff > 0 {
			s.baseBackoff = backoff
		}
	}
}

// NewSupervisor creates a new lifecycle Supervisor.
func NewSupervisor(opts ...SupervisorOption) *Supervisor {
	s := &Supervisor{
		name:        DefaultSupervisorName,
		maxWorkers:  DefaultMaxWorkers,
		baseBackoff: DefaultBaseBackoff,
		state:       StateCreated,
		workers:     make(map[string]*workerEntry),
		stopDone:    make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Supervisor) Name() string           { return s.name }
func (s *Supervisor) Dependencies() []string { return nil }

// SetPanicReporter configures the reporter for recovered panics.
func (s *Supervisor) SetPanicReporter(reporter PanicReporter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reporter = reporter
}

// Register registers a worker spec with the supervisor.
// If the supervisor is already running, the worker starts immediately.
func (s *Supervisor) Register(spec WorkerSpec) error {
	if spec.Name == "" {
		return errors.New("worker name cannot be empty")
	}
	if spec.Run == nil {
		return errors.New("worker run function cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.quiesced || s.state == StateStopping || s.state == StateStopped {
		return errors.New("supervisor is stopping or quiesced")
	}
	if len(s.workers) >= s.maxWorkers {
		return fmt.Errorf("supervisor worker limit exceeded (%d/%d)", len(s.workers), s.maxWorkers)
	}
	if _, exists := s.workers[spec.Name]; exists {
		return fmt.Errorf("worker %q is already registered", spec.Name)
	}

	entry := &workerEntry{
		spec:  spec,
		state: WorkerStateCreated,
	}
	s.workers[spec.Name] = entry
	s.order = append(s.order, spec.Name)

	if s.state == StateRunning {
		s.wg.Add(1)
		go s.runWorker(entry)
	}
	return nil
}

// Go registers and runs a one-shot or non-restarting worker task.
func (s *Supervisor) Go(name string, fn func(context.Context) error) error {
	return s.Register(WorkerSpec{
		Name:    name,
		Restart: NeverRestart,
		Run:     fn,
	})
}

// Start transitions the supervisor to StateRunning and launches all registered workers.
func (s *Supervisor) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.state != StateCreated {
		defer s.mu.Unlock()
		return fmt.Errorf("cannot start supervisor in state %s", s.state)
	}
	s.state = StateRunning
	s.ctx, s.cancel = context.WithCancel(ctx)

	for _, name := range s.order {
		w := s.workers[name]
		s.wg.Add(1)
		go s.runWorker(w)
	}
	s.mu.Unlock()

	return nil
}

// Quiesce stops accepting new workers.
func (s *Supervisor) Quiesce(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quiesced = true
	return nil
}

// Stop gracefully cancels all workers and waits for them to join.
func (s *Supervisor) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.state = StateStopping
		s.quiesced = true
		cancel := s.cancel
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		go func() {
			s.wg.Wait()
			s.mu.Lock()
			s.state = StateStopped
			s.mu.Unlock()
			close(s.stopDone)
		}()
	})

	select {
	case <-s.stopDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Health evaluates the health of the supervisor and all registered workers.
func (s *Supervisor) Health(ctx context.Context) ComponentHealth {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.state == StateFailed {
		return ComponentHealth{Status: HealthUnhealthy, Details: "supervisor in failed state"}
	}

	var degradedWorkers []string
	for _, name := range s.order {
		w := s.workers[name]
		w.mu.Lock()
		st := w.state
		lastErr := w.lastError
		w.mu.Unlock()

		if st == WorkerStateDegraded || st == WorkerStateFailed {
			if lastErr != nil {
				degradedWorkers = append(degradedWorkers, fmt.Sprintf("%s (%v)", name, lastErr))
			} else {
				degradedWorkers = append(degradedWorkers, name)
			}
		}
	}

	if len(degradedWorkers) > 0 {
		return ComponentHealth{
			Status:  HealthDegraded,
			Details: fmt.Sprintf("workers failed or degraded: %v", degradedWorkers),
		}
	}

	return ComponentHealth{
		Status:  HealthHealthy,
		Details: fmt.Sprintf("%d worker(s) registered", len(s.workers)),
	}
}

// Snapshot returns a snapshot of all worker states.
func (s *Supervisor) Snapshot() []WorkerSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshots := make([]WorkerSnapshot, 0, len(s.order))
	for _, name := range s.order {
		w := s.workers[name]
		snapshots = append(snapshots, w.snapshot())
	}
	return snapshots
}

// Workers returns a snapshot of all worker states (alias for Snapshot).
func (s *Supervisor) Workers() []WorkerSnapshot {
	return s.Snapshot()
}

func (s *Supervisor) runWorker(w *workerEntry) {
	defer s.wg.Done()

	for {
		s.mu.RLock()
		supCtx := s.ctx
		s.mu.RUnlock()

		if supCtx == nil || supCtx.Err() != nil {
			w.markStopped(nil)
			return
		}

		w.markRunning()
		err, panicked := s.invokeWorker(supCtx, w)

		if supCtx.Err() != nil {
			// Supervisor is shutting down; this was a normal exit under cancellation
			w.markStopped(nil)
			return
		}

		if err == nil && !panicked {
			// Normal clean completion
			w.markStopped(nil)
			return
		}

		// Failure occurred
		w.recordFailure(err, panicked)

		if w.spec.Restart == NeverRestart {
			w.markFailed(err)
			return
		}

		// RestartTransient: evaluate sliding window budget
		now := time.Now()
		window := w.spec.RestartWindow
		if window <= 0 {
			window = DefaultRestartWindow
		}
		maxRestarts := w.spec.MaxRestarts
		if maxRestarts <= 0 {
			maxRestarts = DefaultMaxRestarts
		}

		allowed, attemptInWindow := w.checkAndRecordRestart(now, window, maxRestarts)
		if !allowed {
			w.markDegraded(fmt.Errorf("worker %s exceeded restart budget (%d restarts within %v): %w", w.spec.Name, maxRestarts, window, err))
			return
		}

		// Calculate exponential backoff with jitter
		backoff := s.calculateBackoff(attemptInWindow)
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-supCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			w.markStopped(nil)
			return
		}
	}
}

func (s *Supervisor) invokeWorker(ctx context.Context, w *workerEntry) (err error, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			err = fmt.Errorf("panic: %v", r)
			w.mu.Lock()
			w.panics++
			w.mu.Unlock()

			s.mu.RLock()
			rep := s.reporter
			name := s.name
			s.mu.RUnlock()

			if rep != nil {
				rep.ReportPanic(PanicReport{
					Owner:     name,
					Component: w.spec.Name,
					Value:     r,
					Stack:     debug.Stack(),
					At:        time.Now().UTC(),
				})
			}
		}
	}()

	err = w.spec.Run(ctx)
	return err, false
}

func (s *Supervisor) calculateBackoff(attempt int) time.Duration {
	base := s.baseBackoff
	if base <= 0 {
		base = DefaultBaseBackoff
	}
	shift := attempt
	if shift > 5 {
		shift = 5
	}
	backoff := base * (1 << shift)
	if backoff > 2*time.Second {
		backoff = 2 * time.Second
	}
	// Add jitter up to 25%
	jitter := time.Duration(rand.Int63n(int64(backoff/4) + 1))
	return backoff + jitter
}

type workerEntry struct {
	spec         WorkerSpec
	mu           sync.Mutex
	state        WorkerState
	restarts     []time.Time
	restartCount int
	panics       int
	lastError    error
	startedAt    time.Time
	stoppedAt    time.Time
}

func (w *workerEntry) markRunning() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state = WorkerStateRunning
	w.startedAt = time.Now().UTC()
}

func (w *workerEntry) markStopped(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state = WorkerStateStopped
	w.stoppedAt = time.Now().UTC()
	if err != nil {
		w.lastError = err
	}
}

func (w *workerEntry) markFailed(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state = WorkerStateFailed
	w.stoppedAt = time.Now().UTC()
	w.lastError = err
}

func (w *workerEntry) markDegraded(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state = WorkerStateDegraded
	w.stoppedAt = time.Now().UTC()
	w.lastError = err
}

func (w *workerEntry) recordFailure(err error, panicked bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastError = err
}

func (w *workerEntry) checkAndRecordRestart(now time.Time, window time.Duration, maxRestarts int) (bool, int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.restartCount++

	cutoff := now.Add(-window)
	filtered := make([]time.Time, 0, len(w.restarts))
	for _, t := range w.restarts {
		if t.After(cutoff) {
			filtered = append(filtered, t)
		}
	}
	w.restarts = filtered

	if len(w.restarts) >= maxRestarts {
		return false, len(w.restarts)
	}

	w.restarts = append(w.restarts, now)
	return true, len(w.restarts)
}

func (w *workerEntry) snapshot() WorkerSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()

	var uptime time.Duration
	if w.state == WorkerStateRunning && !w.startedAt.IsZero() {
		uptime = time.Since(w.startedAt)
	}

	var lastErrStr string
	if w.lastError != nil {
		lastErrStr = w.lastError.Error()
	}

	return WorkerSnapshot{
		Name:         w.spec.Name,
		State:        w.state,
		RestartCount: w.restartCount,
		LastError:    lastErrStr,
		Panics:       w.panics,
		Uptime:       uptime,
	}
}
