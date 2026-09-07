package callback

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// StateScope describes authorization and lifecycle metadata for a callback state entry.
type StateScope struct {
	UserID    int64
	ChatID    int64
	MessageID int
	Namespace string
	SingleUse bool
	ExpiresAt time.Time
}

// StateEntry is the stored value plus scope returned to callers.
type StateEntry struct {
	Data  any
	Scope StateScope
}

// stateItem internal storage.
type stateItem struct {
	data      any
	scope     StateScope
	expiresAt time.Time
	consumed  bool
}

// StateStore is a thread-safe in-memory cache for temporary callback payload states.
type StateStore struct {
	mu     sync.RWMutex
	items  map[string]stateItem
	cancel context.CancelFunc
}

// NewStateStore creates an initialized StateStore.
func NewStateStore() *StateStore {
	return &StateStore{
		items: make(map[string]stateItem),
	}
}

// Store records arbitrary state data with an optional authorized user restriction and TTL.
// Returns a short opaque ID safe for compact Telegram callback data payloads.
func (s *StateStore) Store(data any, allowedUserID int64, ttl time.Duration) string {
	scope := StateScope{UserID: allowedUserID}
	return s.StoreWithScope(data, scope, ttl)
}

const maxStateStoreEntries = 5000

// StoreWithScope records state with full scope metadata.
func (s *StateStore) StoreWithScope(data any, scope StateScope, ttl time.Duration) string {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	opaqueID := hex.EncodeToString(b)

	expiresAt := time.Now().Add(ttl)
	scope.ExpiresAt = expiresAt

	s.mu.Lock()
	defer s.mu.Unlock()

	// Memory limit: enforce max entries, evict expired first, then oldest
	if len(s.items) >= maxStateStoreEntries {
		// try prune
		now := time.Now()
		for id, item := range s.items {
			if now.After(item.expiresAt) {
				delete(s.items, id)
			}
		}
		if len(s.items) >= maxStateStoreEntries {
			// evict oldest (smallest expiresAt)
			var oldestID string
			var oldestTime time.Time
			first := true
			for id, item := range s.items {
				if first || item.expiresAt.Before(oldestTime) {
					oldestID = id
					oldestTime = item.expiresAt
					first = false
				}
			}
			if oldestID != "" {
				delete(s.items, oldestID)
			}
		}
	}

	s.items[opaqueID] = stateItem{
		data:      data,
		scope:     scope,
		expiresAt: expiresAt,
	}
	return opaqueID
}

// Len returns current entry count (for metrics).
func (s *StateStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// Get retrieves the stored state if not expired. Returns ErrStateNotFound or ErrStateExpired.
func (s *StateStore) Get(opaqueID string) (data any, allowedUserID int64, ok bool) {
	entry, err := s.GetEntry(opaqueID)
	if err != nil {
		return nil, 0, false
	}
	return entry.Data, entry.Scope.UserID, true
}

// GetEntry returns the entry or a typed error distinguishing not found vs expired.
func (s *StateStore) GetEntry(opaqueID string) (StateEntry, error) {
	s.mu.RLock()
	item, exists := s.items[opaqueID]
	s.mu.RUnlock()

	if !exists {
		return StateEntry{}, ErrStateNotFound
	}
	if time.Now().After(item.expiresAt) {
		s.Delete(opaqueID)
		return StateEntry{}, ErrStateExpired
	}
	if item.consumed {
		return StateEntry{}, ErrStateConsumed
	}
	return StateEntry{Data: item.data, Scope: item.scope}, nil
}

// Consume atomically retrieves and marks a single-use entry as consumed.
// Returns ErrStateNotFound / ErrStateExpired / ErrStateConsumed as appropriate.
func (s *StateStore) Consume(opaqueID string) (StateEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, exists := s.items[opaqueID]
	if !exists {
		return StateEntry{}, ErrStateNotFound
	}
	if time.Now().After(item.expiresAt) {
		delete(s.items, opaqueID)
		return StateEntry{}, ErrStateExpired
	}
	if item.consumed {
		return StateEntry{}, ErrStateConsumed
	}
	if item.scope.SingleUse {
		item.consumed = true
		s.items[opaqueID] = item
	}
	return StateEntry{Data: item.data, Scope: item.scope}, nil
}

// Delete removes an item from the store.
func (s *StateStore) Delete(opaqueID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, opaqueID)
}

// Prune sweeps through all items and removes expired states.
func (s *StateStore) Prune() int {
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
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}
