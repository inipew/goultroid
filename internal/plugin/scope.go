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

// Scope owns cancellable plugin work and cleanup callbacks. It is safe for
// concurrent use and can be closed repeatedly.
type Scope struct {
	owner   string
	ctx     context.Context
	cancel  context.CancelFunc
	manager *resource.Manager

	mu        sync.Mutex
	closed    bool
	cleanups  []func()
	resources map[string]Resource
	wg        sync.WaitGroup
	goCounter atomic.Uint64
}

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
		owner:     owner,
		ctx:       ctx,
		cancel:    cancel,
		manager:   manager,
		resources: make(map[string]Resource),
	}
}

func (s *Scope) Context() context.Context { return s.ctx }
func (s *Scope) Owner() string            { return s.owner }

// SetManager binds a ResourceManager to this scope.
func (s *Scope) SetManager(m *resource.Manager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manager = m
}

// Go starts work with the scope context, tracks the goroutine in the ResourceManager,
// and waits for it during Close.
func (s *Scope) Go(fn func(context.Context)) error {
	if fn == nil {
		return errors.New("scope goroutine cannot be nil")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("plugin scope is closed")
	}
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
			if rID != "" && mgr != nil {
				_ = mgr.Release(rID)
			}
			s.wg.Done()
		}()
		fn(s.ctx)
	}()
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

// Close cancels child work, waits up to ctx's deadline, runs cleanup callbacks in
// reverse order, and detects residual resource leaks.
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
	cleanups := append([]func(){}, s.cleanups...)
	s.cleanups = nil
	mgr := s.manager
	s.mu.Unlock()

	s.cancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	var waitErr error
	select {
	case <-done:
	case <-ctx.Done():
		waitErr = ctx.Err()
	}

	for i := len(cleanups) - 1; i >= 0; i-- {
		func() {
			defer func() { _ = recover() }()
			cleanups[i]()
		}()
	}

	if mgr != nil {
		leaks := mgr.DetectLeaks(s.owner)
		if len(leaks) > 0 {
			var leakIDs []string
			for _, l := range leaks {
				leakIDs = append(leakIDs, l.ID)
			}
			leakErr := fmt.Errorf("plugin scope %s leaked %d resource(s): %v", s.owner, len(leaks), leakIDs)
			if waitErr != nil {
				return fmt.Errorf("%w; %v", waitErr, leakErr)
			}
			return leakErr
		}
	}

	if waitErr != nil {
		return fmt.Errorf("close plugin scope %s: %w", s.owner, waitErr)
	}
	return nil
}
