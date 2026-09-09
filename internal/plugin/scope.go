package plugin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Resource describes a long-lived resource owned by a plugin scope.
// Resource registration is intentionally generic so infrastructure packages can
// report subscriptions, jobs, tasks, processes, and temporary files uniformly.
type Resource struct {
	ID        string
	Owner     string
	Type      string
	CreatedAt time.Time
	State     string
}

// Scope owns cancellable plugin work and cleanup callbacks. It is safe for
// concurrent use and can be closed repeatedly.
type Scope struct {
	owner  string
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	closed    bool
	cleanups  []func()
	resources map[string]Resource
	wg        sync.WaitGroup
}

// NewScope creates a child lifecycle scope. A nil parent is treated as a
// background context for compatibility with legacy callers.
func NewScope(parent context.Context, owner string) *Scope {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &Scope{owner: owner, ctx: ctx, cancel: cancel, resources: make(map[string]Resource)}
}

func (s *Scope) Context() context.Context { return s.ctx }
func (s *Scope) Owner() string            { return s.owner }

// Go starts work with the scope context and waits for it during Close.
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
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
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

func (s *Scope) Track(resource Resource) error {
	if resource.ID == "" {
		return errors.New("resource id cannot be empty")
	}
	if resource.Owner == "" {
		resource.Owner = s.owner
	}
	if resource.CreatedAt.IsZero() {
		resource.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("plugin scope is closed")
	}
	s.resources[resource.ID] = resource
	return nil
}

func (s *Scope) Release(resourceID string) {
	s.mu.Lock()
	delete(s.resources, resourceID)
	s.mu.Unlock()
}

func (s *Scope) Resources() []Resource {
	s.mu.Lock()
	defer s.mu.Unlock()
	resources := make([]Resource, 0, len(s.resources))
	for _, resource := range s.resources {
		resources = append(resources, resource)
	}
	return resources
}

// Close cancels child work, waits up to ctx's deadline, and then runs cleanup
// callbacks in reverse order. It is safe to call more than once.
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
	if waitErr != nil {
		return fmt.Errorf("close plugin scope %s: %w", s.owner, waitErr)
	}
	return nil
}
