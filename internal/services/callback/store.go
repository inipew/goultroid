package callback

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// StateStore is a thread-safe in-memory cache for temporary callback payload states.
type StateStore struct {
	mu            sync.RWMutex
	items         map[string]stateItem
	retainedBytes int64
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

const (
	maxStateStoreEntries = 5000
	maxStateStoreBytes   = 8 * 1024 * 1024 // 8MB budget
	maxStateItemBytes    = 64 * 1024       // 64KB max per entry
)

func cloneStateData(data any) any {
	if b, ok := data.([]byte); ok {
		cpy := make([]byte, len(b))
		copy(cpy, b)
		return cpy
	}
	return data
}

func estimateStateSize(data any) int64 {
	const baseOverhead = 64
	switch v := data.(type) {
	case []byte:
		return int64(len(v)) + baseOverhead
	case string:
		return int64(len(v)) + baseOverhead
	default:
		return 256
	}
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

// StoreWithScope records state with full scope metadata.
func (s *StateStore) StoreWithScope(data any, scope StateScope, ttl time.Duration) string {
	size := estimateStateSize(data)
	if size > maxStateItemBytes {
		return ""
	}
	cloned := cloneStateData(data)

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

	if len(s.items) >= maxStateStoreEntries || s.retainedBytes+size > maxStateStoreBytes {
		now := time.Now()
		for id, item := range s.items {
			if now.After(item.expiresAt) {
				s.retainedBytes -= item.sizeBytes
				delete(s.items, id)
			}
		}
		for len(s.items) >= maxStateStoreEntries || (len(s.items) > 0 && s.retainedBytes+size > maxStateStoreBytes) {
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
				s.retainedBytes -= s.items[oldestID].sizeBytes
				delete(s.items, oldestID)
			} else {
				break
			}
		}
	}

	s.items[opaqueID] = stateItem{
		data:      cloned,
		scope:     scope,
		expiresAt: expiresAt,
		sizeBytes: size,
	}
	s.retainedBytes += size
	return opaqueID
}

// Len returns current entry count (for metrics).
func (s *StateStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// RetainedBytes returns current retained byte size (for metrics/testing).
func (s *StateStore) RetainedBytes() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.retainedBytes
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
	return StateEntry{Data: cloneStateData(item.data), Scope: item.scope}, nil
}

// Consume atomically retrieves and marks a single-use entry as consumed.
func (s *StateStore) Consume(opaqueID string) (StateEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, exists := s.items[opaqueID]
	if !exists {
		return StateEntry{}, ErrStateNotFound
	}
	if time.Now().After(item.expiresAt) {
		s.retainedBytes -= item.sizeBytes
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
	return StateEntry{Data: cloneStateData(item.data), Scope: item.scope}, nil
}

// Delete removes an item from the store.
func (s *StateStore) Delete(opaqueID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item, exists := s.items[opaqueID]; exists {
		s.retainedBytes -= item.sizeBytes
		delete(s.items, opaqueID)
	}
}
