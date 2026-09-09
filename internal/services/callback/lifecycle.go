package callback

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
)

var _ runtime.Component = (*StateStore)(nil)

// Name returns component identifier for runtime.Component.
func (s *StateStore) Name() string {
	return "callback_store"
}

// Dependencies returns prerequisite components for runtime.Component.
func (s *StateStore) Dependencies() []string {
	return []string{"workers"}
}

// Health probes the health status of the state store.
func (s *StateStore) Health(ctx context.Context) runtime.ComponentHealth {
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}

// Prune sweeps through all items and removes expired states.
func (s *StateStore) Prune() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	pruned := 0
	for id, item := range s.items {
		if now.After(item.expiresAt) {
			delete(s.items, id)
			pruned++
		}
	}
	return pruned
}

// Start launches a background goroutine that periodically prunes expired entries.
// It is safe to call multiple times; subsequent calls are no-op.
func (s *StateStore) Start(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.Prune()
			case <-runCtx.Done():
				return
			}
		}
	}()
	return nil
}

// Stop terminates the background pruning goroutine.
func (s *StateStore) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
		done := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(done)
		}()
		if ctx != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		} else {
			<-done
		}
	}
	return nil
}
