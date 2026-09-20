package plugin

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
)

// Resource describes a long-lived resource owned by a plugin scope.
// Aliased to resource.Resource for unified platform tracking and backward compatibility.
type Resource = resource.Resource

// DefaultMaxScopeGoroutines is the maximum concurrent unmanaged goroutines permitted per scope.
const DefaultMaxScopeGoroutines = 64

// CleanupFunc is a scope-owned teardown callback. Implementations must honor
// ctx cancellation; Scope.Close also enforces the caller deadline around
// callbacks so legacy/non-cooperative cleanup cannot stall global shutdown.
type CleanupFunc func(context.Context) error

// Scope owns cancellable plugin work and cleanup callbacks. It is safe for
// concurrent use and can be closed repeatedly.
type Scope struct {
	owner           string
	generation      uint64
	ctx             context.Context
	cancel          context.CancelFunc
	manager         *resource.Manager
	panicReporter   core.PanicReporter
	cleanupExecutor *runtime.CallbackExecutor

	mu               sync.Mutex
	closed           bool
	cleanups         []CleanupFunc
	resources        map[string]Resource
	activeGoroutines int
	maxGoroutines    int
	idle             chan struct{}
	goCounter        atomic.Uint64
	panicCount       atomic.Uint64
}

var scopeGeneration atomic.Uint64

// NewScope creates a child lifecycle scope. A nil parent is treated as a
// background context for compatibility with legacy callers.
func NewScope(parent context.Context, owner string) *Scope {
	return NewScopeWithManager(parent, owner, nil)
}

// NewScopeWithManager creates a child lifecycle scope backed by a ResourceManager.
func NewScopeWithManager(parent context.Context, owner string, manager *resource.Manager) *Scope {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	idle := make(chan struct{})
	close(idle)
	return &Scope{
		owner:           owner,
		generation:      scopeGeneration.Add(1),
		ctx:             ctx,
		cancel:          cancel,
		manager:         manager,
		cleanupExecutor: runtime.NewCallbackExecutor(runtime.DefaultLifecycleCallbackConcurrency),
		maxGoroutines:   DefaultMaxScopeGoroutines,
		resources:       make(map[string]Resource),
		idle:            idle,
	}
}

func (s *Scope) Context() context.Context { return s.ctx }
func (s *Scope) Owner() string            { return s.owner }
func (s *Scope) Generation() uint64       { return s.generation }

// SetManager binds a ResourceManager to this scope.
func (s *Scope) SetManager(m *resource.Manager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manager = m
}

// SetCleanupExecutor shares the application lifecycle callback budget with this scope.
func (s *Scope) SetCleanupExecutor(executor *runtime.CallbackExecutor) {
	if executor == nil {
		return
	}
	s.mu.Lock()
	s.cleanupExecutor = executor
	s.mu.Unlock()
}

// SetMaxGoroutines sets the maximum concurrent goroutines allowed in Scope.Go.
func (s *Scope) SetMaxGoroutines(max int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxGoroutines = max
}

// Go starts work with the scope context, tracks the goroutine in the ResourceManager,
// and waits for it during Close. It enforces the maximum goroutine budget.
func (s *Scope) Go(fn func(context.Context)) error {
	if fn == nil {
		return errors.New("scope goroutine cannot be nil")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("plugin scope is closed")
	}
	limit := s.maxGoroutines
	if limit <= 0 {
		limit = DefaultMaxScopeGoroutines
	}
	if s.activeGoroutines >= limit {
		s.mu.Unlock()
		return fmt.Errorf("plugin scope goroutine limit exceeded (%d/%d)", s.activeGoroutines, limit)
	}
	if s.activeGoroutines == 0 {
		s.idle = make(chan struct{})
	}
	s.activeGoroutines++
	mgr := s.manager
	s.mu.Unlock()

	var rID string
	if mgr != nil {
		rID = fmt.Sprintf("goroutine:%s:%d", s.owner, s.goCounter.Add(1))
		if err := mgr.Register(resource.Resource{
			ID:        rID,
			Owner:     s.owner,
			Type:      resource.TypeGoroutine,
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			s.mu.Lock()
			s.activeGoroutines--
			if s.activeGoroutines == 0 {
				close(s.idle)
			}
			s.mu.Unlock()
			return fmt.Errorf("track plugin goroutine: %w", err)
		}
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.panicCount.Add(1)
				s.mu.Lock()
				reporter := s.panicReporter
				s.mu.Unlock()
				if reporter != nil {
					reporter.ReportPanic(core.PanicReport{
						Owner:     s.owner,
						Component: "plugin.Scope.Go",
						Value:     r,
						Stack:     debug.Stack(),
						At:        time.Now().UTC(),
					})
				}
			}

			// idle is the Scope.Close completion barrier. Remove the goroutine's
			// global resource record before publishing activeGoroutines == 0;
			// otherwise Close can observe idle and race DetectLeaks against Release.
			if rID != "" && mgr != nil {
				_ = mgr.Release(rID)
			}

			s.mu.Lock()
			s.activeGoroutines--
			if s.activeGoroutines == 0 {
				close(s.idle)
			}
			s.mu.Unlock()
		}()
		fn(s.ctx)
	}()
	return nil
}

// SetPanicReporter configures the reporter for recovered goroutine panics.
func (s *Scope) SetPanicReporter(reporter core.PanicReporter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panicReporter = reporter
}

// Panics returns the total number of panics recovered by this scope.
func (s *Scope) Panics() uint64 {
	return s.panicCount.Load()
}

// ActiveGoroutines returns the number of currently running goroutines in this scope.
func (s *Scope) ActiveGoroutines() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeGoroutines
}

// DeferContext registers a context-aware idempotent-at-scope cleanup callback.
// Callbacks execute in reverse registration order during Close.
func (s *Scope) DeferContext(fn CleanupFunc) error {
	if fn == nil {
		return errors.New("scope cleanup cannot be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("plugin scope is closed")
	}
	s.cleanups = append(s.cleanups, fn)
	return nil
}

// Defer is the compatibility form for legacy contextless cleanup. Close still
// bounds the callback by its context; callers implementing cancellable cleanup
// should prefer DeferContext.
func (s *Scope) Defer(fn func()) error {
	if fn == nil {
		return errors.New("scope cleanup cannot be nil")
	}
	return s.DeferContext(func(context.Context) error {
		fn()
		return nil
	})
}

// SubscribeEvent registers an event listener on the EventBus bound to this scope.
// When the scope closes, the subscription is automatically terminated.
func (s *Scope) SubscribeEvent(bus *core.EventBus, eventType core.EventType, handler core.EventHandler) *core.Subscription {
	if bus == nil || handler == nil {
		return nil
	}
	sub := bus.SubscribeContextScoped(s.ctx, s.owner, tasks.ScopeIdentity{Owner: s.owner, Generation: s.generation}, eventType, handler)
	if sub != nil {
		subID := fmt.Sprintf("sub:%s:%s", s.owner, eventType)
		_ = s.Track(Resource{
			ID:    subID,
			Owner: s.owner,
			Type:  resource.TypeSubscription,
		})
		_ = s.DeferContext(func(context.Context) error {
			sub.Close()
			s.Release(subID)
			return nil
		})
	}
	return sub
}

// TrackWebSocket registers a managed WebSocket connection within this scope.
// When the scope closes, closeFn is automatically invoked to gracefully disconnect the socket.
func (s *Scope) TrackWebSocket(wsID, url string, closeFn func() error) error {
	cleanID := strings.TrimSpace(wsID)
	if cleanID == "" {
		return errors.New("websocket id cannot be empty")
	}
	resID := fmt.Sprintf("ws:%s:%s", s.owner, cleanID)
	meta := make(map[string]string)
	if url != "" {
		meta["url"] = url
	}
	r := Resource{
		ID:        resID,
		Owner:     s.owner,
		Type:      resource.TypeWebSocket,
		CreatedAt: time.Now().UTC(),
		Metadata:  meta,
	}
	if err := s.Track(r); err != nil {
		return err
	}
	if closeFn != nil {
		_ = s.DeferContext(func(context.Context) error {
			err := closeFn()
			s.Release(resID)
			return err
		})
	}
	return nil
}

// Track registers an active resource with this scope and the global ResourceManager.
func (s *Scope) Track(r Resource) error {
	if r.ID == "" {
		return errors.New("resource id cannot be empty")
	}
	if r.Owner == "" {
		r.Owner = s.owner
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("plugin scope is closed")
	}
	if _, exists := s.resources[r.ID]; exists {
		s.mu.Unlock()
		return fmt.Errorf("resource %q already tracked by plugin scope", r.ID)
	}
	s.resources[r.ID] = r
	mgr := s.manager
	s.mu.Unlock()

	if mgr != nil {
		if err := mgr.Register(r); err != nil {
			s.mu.Lock()
			delete(s.resources, r.ID)
			s.mu.Unlock()
			return fmt.Errorf("register scoped resource: %w", err)
		}
	}
	return nil
}

// Release unregisters a resource from tracking.
func (s *Scope) Release(resourceID string) {
	s.mu.Lock()
	delete(s.resources, resourceID)
	mgr := s.manager
	s.mu.Unlock()

	if mgr != nil {
		_ = mgr.Release(resourceID)
	}
}

// Resources returns a copy of all resources tracked by this scope.
func (s *Scope) Resources() []Resource {
	s.mu.Lock()
	defer s.mu.Unlock()
	resources := make([]Resource, 0, len(s.resources))
	for _, res := range s.resources {
		resources = append(resources, res)
	}
	return resources
}

func (s *Scope) runCleanup(ctx context.Context, cleanup CleanupFunc) error {
	if cleanup == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	executor := s.cleanupExecutor
	reporter := s.panicReporter
	s.mu.Unlock()
	if executor == nil {
		executor = runtime.NewCallbackExecutor(runtime.DefaultLifecycleCallbackConcurrency)
	}

	err := executor.Run(ctx, func() error { return cleanup(ctx) })
	var panicErr *runtime.CallbackPanicError
	if errors.As(err, &panicErr) {
		s.panicCount.Add(1)
		if reporter != nil {
			reporter.ReportPanic(core.PanicReport{
				Owner:     s.owner,
				Component: "plugin.Scope.Close",
				Value:     panicErr.Value,
				Stack:     panicErr.Stack,
				At:        time.Now().UTC(),
			})
		}
		// CallbackExecutor has already contained the panic. Treat reporting as the
		// terminal handling contract so one bad cleanup cannot make Scope.Close
		// fail twice while still allowing later cleanups and leak detection to run.
		return nil
	}
	return err
}

// Close cancels child work, waits up to ctx's deadline, runs cleanup callbacks in
// reverse order, and detects residual resource leaks. The caller deadline is
// authoritative even for legacy cleanup callbacks that do not accept context.
func (s *Scope) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cleanups := append([]CleanupFunc(nil), s.cleanups...)
	s.cleanups = nil
	mgr := s.manager
	done := s.idle
	s.mu.Unlock()

	s.cancel()

	var waitErr error
	select {
	case <-done:
	case <-ctx.Done():
		waitErr = ctx.Err()
	}

	var cleanupErrs []error
	for i := len(cleanups) - 1; i >= 0; i-- {
		if err := s.runCleanup(ctx, cleanups[i]); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("cleanup %d: %w", i, err))
			if ctx.Err() != nil {
				break
			}
		}
	}

	if mgr != nil {
		leaks := mgr.DetectLeaks(s.owner)
		if len(leaks) > 0 {
			var leakIDs []string
			for _, l := range leaks {
				leakIDs = append(leakIDs, l.ID)
			}
			leakErr := fmt.Errorf("plugin scope %s leaked %d resource(s): %v", s.owner, len(leaks), leakIDs)
			cleanupErrs = append(cleanupErrs, leakErr)
		}
	}

	if waitErr != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("close plugin scope %s: %w", s.owner, waitErr))
	}
	if len(cleanupErrs) > 0 {
		return errors.Join(cleanupErrs...)
	}
	return nil
}
