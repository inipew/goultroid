package callback

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// stateItem represents a cached callback state entry.
type stateItem struct {
	data          any
	allowedUserID int64
	expiresAt     time.Time
}

// StateStore is a thread-safe in-memory cache for temporary callback payload states.
type StateStore struct {
	mu    sync.RWMutex
	items map[string]stateItem
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
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}

	b := make([]byte, 8)
	_, _ = rand.Read(b)
	opaqueID := hex.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.items[opaqueID] = stateItem{
		data:          data,
		allowedUserID: allowedUserID,
		expiresAt:     time.Now().Add(ttl),
	}

	return opaqueID
}

// Get retrieves the stored state and authorized user ID if not expired.
func (s *StateStore) Get(opaqueID string) (data any, allowedUserID int64, ok bool) {
	s.mu.RLock()
	item, exists := s.items[opaqueID]
	s.mu.RUnlock()

	if !exists {
		return nil, 0, false
	}

	if time.Now().After(item.expiresAt) {
		s.Delete(opaqueID)
		return nil, 0, false
	}

	return item.data, item.allowedUserID, true
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
