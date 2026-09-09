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
	repo    Repository
	stopCh  chan struct{}
}

// NewManager creates an in-memory Idempotency Manager and starts a background eviction loop.
func NewManager(cleanupInterval time.Duration, repositories ...Repository) *Manager {
	if cleanupInterval <= 0 {
		cleanupInterval = 1 * time.Minute
	}

	m := &Manager{
		entries: make(map[string]entry),
		stopCh:  make(chan struct{}),
	}
	if len(repositories) > 0 {
		m.repo = repositories[0]
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
	if m.repo != nil {
		_, _ = m.repo.DeleteExpired(context.Background(), now)
		return
	}
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
	if m.repo != nil {
		return m.repo.Claim(ctx, cleanKey, now, now.Add(ttl))
	}
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
	if m.repo != nil {
		processed, err := m.repo.IsProcessed(context.Background(), cleanKey, time.Now().UTC())
		return err == nil && processed
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
	if m.repo != nil {
		size, err := m.repo.Size(context.Background(), time.Now().UTC())
		if err == nil {
			return size
		}
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}
