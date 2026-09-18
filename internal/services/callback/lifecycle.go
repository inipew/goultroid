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
	return []string{"taskengine"}
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
			s.retainedBytes -= item.sizeBytes
			delete(s.items, id)
			pruned++
		}
	}
	return pruned
}

// Start is intentionally passive. StateStore expiration is opportunistic:
// Get/Consume delete touched expired entries, Store prunes under pressure, and
// Len/Prune perform explicit sweeps. This avoids periodic idle wakeups.
func (s *StateStore) Start(context.Context) error {
	return nil
}

// Stop is a no-op because StateStore owns no background goroutines.
func (s *StateStore) Stop(context.Context) error {
	return nil
}
