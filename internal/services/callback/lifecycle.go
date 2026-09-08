package callback

import (
	"context"
	"time"
)

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
func (s *StateStore) Start(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mu.Unlock()

	go func() {
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
}

// Stop terminates the background pruning goroutine.
func (s *StateStore) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}
