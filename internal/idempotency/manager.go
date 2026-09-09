package idempotency

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrDuplicateExecution = errors.New("duplicate execution detected by idempotency manager")
)

type entry struct {
	key       string
	createdAt time.Time
	expiresAt time.Time
}

// Manager manages idempotency keys and deduplication windows to prevent duplicate side effects.
type Manager struct {
	mu      sync.RWMutex
	entries map[string]entry
	stopCh  chan struct{}
}

// NewManager creates an in-memory Idempotency Manager and starts a background eviction loop.
func NewManager(cleanupInterval time.Duration) *Manager {
	if cleanupInterval <= 0 {
		cleanupInterval = 1 * time.Minute
	}

	m := &Manager{
		entries: make(map[string]entry),
		stopCh:  make(chan struct{}),
	}

	go m.cleanupLoop(cleanupInterval)
	return m
}

func (m *Manager) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.evictExpired()
		}
	}
}

func (m *Manager) evictExpired() {
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()

	for k, e := range m.entries {
		if now.After(e.expiresAt) {
			delete(m.entries, k)
		}
	}
}

// Close stops the background eviction loop.
func (m *Manager) Close() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
}

// CheckAndSet returns true if the key was NOT previously seen within its TTL (i.e. first time),
// and records it with the specified TTL. Returns false if the key was already processed (duplicate).
func (m *Manager) CheckAndSet(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	cleanKey := strings.TrimSpace(key)
	if cleanKey == "" {
		return false, errors.New("idempotency key cannot be empty")
	}

	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()

	if e, exists := m.entries[cleanKey]; exists {
		if now.Before(e.expiresAt) {
			return false, nil // Duplicate!
		}
	}

	m.entries[cleanKey] = entry{
		key:       cleanKey,
		createdAt: now,
		expiresAt: now.Add(ttl),
	}
	return true, nil
}

// IsProcessed checks whether a key is currently marked as processed without setting it.
func (m *Manager) IsProcessed(key string) bool {
	cleanKey := strings.TrimSpace(key)
	if cleanKey == "" {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	e, exists := m.entries[cleanKey]
	if !exists {
		return false
	}
	return time.Now().UTC().Before(e.expiresAt)
}

// Size returns the count of currently held keys.
func (m *Manager) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}
