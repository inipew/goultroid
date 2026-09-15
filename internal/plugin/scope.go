package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/resource"
)

// Resource describes a long-lived resource owned by a plugin scope.
// Aliased to resource.Resource for unified platform tracking and backward compatibility.
type Resource = resource.Resource

// DefaultMaxScopeGoroutines is the maximum concurrent unmanaged goroutines permitted per scope.
const DefaultMaxScopeGoroutines = 64

// Scope owns cancellable plugin work and cleanup callbacks. It is safe for
// concurrent use and can be closed repeatedly. Cancellation hooks run before
// the join phase so external execution domains (TaskEngine, jobs, etc.) can be
// fenced before resources are released.
type Scope struct {
	owner      string
	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc
	manager    *resource.Manager

	mu               sync.Mutex
	closed           bool
	closeStarted     bool
	closeDone        chan struct{}
	closeErr         error
	cancelHooks      []func()
	cleanups         []func()
	resources        map[string]Resource
	activeGoroutines int
	maxGoroutines    int
	wg               sync.WaitGroup
	goCounter        atomic.Uint64
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
	return &Scope{
		owner:         owner,
		generation:    scopeGeneration.Add(1),
		ctx:           ctx,
		cancel:        cancel,
		manager:       manager,
		maxGoroutines: DefaultMaxScopeGoroutines,
		resources:     make(map[string]Resource),
		closeDone:     make(chan struct{}),
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

// SetMaxGoroutines sets the maximum concurrent goroutines allowed in Scope.Go.
func (s *Scope) SetMaxGoroutines(max int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxGoroutines = max
}

// Go starts long-lived/service work with the scope context, tracks the
// goroutine in the ResourceManager, and joins it during Close. Finite units of
// execution should use TaskClient instead so admission, fairness and accounting
// remain centralized in TaskEngine.
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
	s.activeGoroutines++
	s.wg.Add(1)
	mgr := s.manager
	s.mu.Unlock()

	var rID string
	if mgr != nil {
		rID = fmt.Sprintf("goroutine:%s:%d", s.owner, s.goCounter.Add(1))
		_ = mgr.Register(resource.Resource{
			ID:        rID,
			Owner:     s.owner,
			Type:      resource.TypeGoroutine,
			CreatedAt: time.Now().UTC(),
		})
	}

	go func() {
		defer func() {
			s.mu.Lock()
			s.activeGoroutines--
			s.mu.Unlock()
			if rID != "" && mgr != nil {
				_ = mgr.Release(rID)
			}
			s.wg.Done()
		}()
		fn(s.ctx)
	}()
	return nil
}

// OnCancel registers a hook that executes exactly once immediately after the
// scope context is cancelled and before Close waits for children. This is used
// to fence external execution domains that are not children of s.ctx after
// admission (for example accepted TaskEngine work).
func (s *Scope) OnCancel(fn func()) error {
	if fn == nil {
		return errors.New("scope cancel hook cannot be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("plugin scope is closed")
	}
	s.cancelHooks = append(s.cancelHooks, fn)
	return nil
}

// Defer registers an idempotent-at-scope cleanup callback. Callbacks execute
// in reverse registration order during Close.
func (s *Scope) Defer(fn func()) error {
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

// SubscribeEvent registers an event listener on the EventBus bound to this scope.
// When the scope closes, the subscription is automatically terminated.
func (s *Scope) SubscribeEvent(bus *core.EventBus, eventType core.EventType, handler core.EventHandler) *core.Subscription {
	if bus == nil || handler == nil {
		return nil
	}
	sub := bus.SubscribeContext(s.ctx, s.owner, eventType, handler)
	if sub != nil {
		subID := fmt.Sprintf("sub:%s:%s", s.owner, eventType)
		_ = s.Track(Resource{
			ID:    subID,
			Owner: s.owner,
			Type:  resource.TypeSubscription,
		})
		_ = s.Defer(func() {
			sub.Close()
			s.Release(subID)
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
		_ = s.Defer(func() {
			_ = closeFn()
			s.Release(resID)
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
	s.resources[r.ID] = r
	mgr := s.manager
	s.mu.Unlock()

	if mgr != nil {
		_ = mgr.Register(r)
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

// Close cancels child/external work exactly once, joins it under the caller's
// deadline, runs cleanup callbacks, and detects residual resource leaks.
// If one caller times out, later callers join the same teardown instead of
// spawning another waiter or reporting a false success.
func (s *Scope) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.closeStarted {
		done := s.closeDone
		s.mu.Unlock()
		select {
		case <-done:
			s.mu.Lock()
			err := s.closeErr
			s.mu.Unlock()
			return err
		case <-ctx.Done():
			return fmt.Errorf("close plugin scope %s: %w", s.owner, ctx.Err())
		}
	}
	s.closeStarted = true
	s.closed = true
	cancelHooks := append([]func(){}, s.cancelHooks...)
	s.cancelHooks = nil
	cleanups := append([]func(){}, s.cleanups...)
	s.cleanups = nil
	mgr := s.manager
	done := s.closeDone
	s.mu.Unlock()

	s.cancel()
	for i := len(cancelHooks) - 1; i >= 0; i-- {
		func() {
			defer func() { _ = recover() }()
			cancelHooks[i]()
		}()
	}

	// Only the first Close owns this waiter. It may outlive the first caller's
	// deadline, but repeated Close calls share it and therefore cannot leak one
	// goroutine per timeout.
	go func() {
		s.wg.Wait()

		for i := len(cleanups) - 1; i >= 0; i-- {
			func() {
				defer func() { _ = recover() }()
				cleanups[i]()
			}()
		}

		var finalErr error
		if mgr != nil {
			leaks := mgr.DetectLeaks(s.owner)
			if len(leaks) > 0 {
				leakIDs := make([]string, 0, len(leaks))
				for _, l := range leaks {
					leakIDs = append(leakIDs, l.ID)
				}
				finalErr = fmt.Errorf("plugin scope %s leaked %d resource(s): %v", s.owner, len(leaks), leakIDs)
			}
		}
		s.mu.Lock()
		s.closeErr = finalErr
		s.mu.Unlock()
		close(done)
	}()

	select {
	case <-done:
		s.mu.Lock()
		err := s.closeErr
		s.mu.Unlock()
		return err
	case <-ctx.Done():
		return fmt.Errorf("close plugin scope %s: %w", s.owner, ctx.Err())
	}
}
