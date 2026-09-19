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
// Construction is passive; background cleanup is owned by Start/Stop.
type Manager struct {
	mu      sync.RWMutex
	entries map[string]entry
	repo    Repository

	lifecycleMu    sync.Mutex
	cleanupInterval time.Duration
	cleanupWake     chan struct{}
	runCtx          context.Context
	cancel          context.CancelFunc
	workerDone      chan struct{}
	started         bool
	workerRunning   bool
}

// NewManager creates an idempotency manager without starting background work.
func NewManager(cleanupInterval time.Duration, repositories ...Repository) *Manager {
	if cleanupInterval <= 0 {
		cleanupInterval = time.Minute
	}
	m := &Manager{
		entries:         make(map[string]entry),
		cleanupInterval: cleanupInterval,
		cleanupWake:     make(chan struct{}, 1),
	}
	if len(repositories) > 0 {
		m.repo = repositories[0]
	}
	return m
}

// Start activates lifecycle ownership. The deadline coordinator is lazy: an
// empty manager keeps zero cleanup goroutines and starts one only when durable
// or in-memory expiry state exists.
func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return errors.New("idempotency manager is nil")
	}
	if ctx == nil {
		return errors.New("idempotency start context is nil")
	}

	m.lifecycleMu.Lock()
	if m.started {
		m.lifecycleMu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.runCtx = runCtx
	m.cancel = cancel
	m.cleanupWake = make(chan struct{}, 1)
	m.started = true
	m.lifecycleMu.Unlock()

	_, found, err := m.nextExpiry(runCtx)
	if err != nil || found {
		m.signalCleanup()
	}
	return nil
}

func (m *Manager) signalCleanup() {
	if m == nil {
		return
	}
	m.lifecycleMu.Lock()
	if !m.started || m.runCtx == nil || m.runCtx.Err() != nil {
		m.lifecycleMu.Unlock()
		return
	}
	if !m.workerRunning {
		done := make(chan struct{})
		m.workerRunning = true
		m.workerDone = done
		runCtx := m.runCtx
		wake := m.cleanupWake
		go m.cleanupLoop(runCtx, wake, done)
	}
	wake := m.cleanupWake
	m.lifecycleMu.Unlock()

	select {
	case wake <- struct{}{}:
	default:
	}
}

func (m *Manager) nextExpiry(ctx context.Context) (time.Time, bool, error) {
	if m.repo != nil {
		return m.repo.EarliestExpiry(ctx)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var next time.Time
	for _, e := range m.entries {
		if next.IsZero() || e.expiresAt.Before(next) {
			next = e.expiresAt
		}
	}
	return next, !next.IsZero(), nil
}

func (m *Manager) cleanupLoop(ctx context.Context, wake <-chan struct{}, done chan struct{}) {
	defer func() {
		m.lifecycleMu.Lock()
		if m.workerDone == done {
			m.workerRunning = false
			m.workerDone = nil
		}
		m.lifecycleMu.Unlock()
		close(done)
	}()

	for {
		next, found, err := m.nextExpiry(ctx)
		if err != nil {
			// Repository failures are an active degraded state, not normal idle.
			// Keep one bounded worker while the deadline source is unreadable.
			timer := time.NewTimer(m.cleanupInterval)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select { case <-timer.C: default: }
				}
				return
			case <-wake:
				if !timer.Stop() {
					select { case <-timer.C: default: }
				}
				continue
			case <-timer.C:
				continue
			}
		}
		if !found {
			// Do not leave a coordinator parked on an empty cache. A concurrent
			// successful Claim either queues a wake before this decision or sees
			// workerRunning=false and starts the next generation.
			m.lifecycleMu.Lock()
			if m.workerDone != done {
				m.lifecycleMu.Unlock()
				return
			}
			select {
			case <-wake:
				m.lifecycleMu.Unlock()
				continue
			default:
			}
			m.workerRunning = false
			m.workerDone = nil
			m.lifecycleMu.Unlock()
			return
		}

		wait := time.Until(next)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select { case <-timer.C: default: }
			}
			return
		case <-wake:
			if !timer.Stop() {
				select { case <-timer.C: default: }
			}
			continue
		case <-timer.C:
			m.evictExpired(ctx)
		}
	}
}

func (m *Manager) evictExpired(ctx context.Context) {
	now := time.Now().UTC()
	if m.repo != nil {
		if ctx == nil || ctx.Err() != nil {
			return
		}
		_, _ = m.repo.DeleteExpired(ctx, now)
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

// Stop cancels cleanup lifecycle and joins the coordinator only when one is
// currently active.
func (m *Manager) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("idempotency stop context is nil")
	}

	m.lifecycleMu.Lock()
	if !m.started {
		m.lifecycleMu.Unlock()
		return nil
	}
	m.started = false
	cancel := m.cancel
	done := m.workerDone
	m.cancel = nil
	m.runCtx = nil
	m.lifecycleMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close is a bounded compatibility wrapper for older callers.
func (m *Manager) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = m.Stop(ctx)
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
		claimed, err := m.repo.Claim(ctx, cleanKey, now, now.Add(ttl))
		if err == nil && claimed {
			m.signalCleanup()
		}
		return claimed, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if e, exists := m.entries[cleanKey]; exists {
		if now.Before(e.expiresAt) {
			return false, nil
		}
	}

	m.entries[cleanKey] = entry{
		key:       cleanKey,
		createdAt: now,
		expiresAt: now.Add(ttl),
	}
	m.signalCleanup()
	return true, nil
}

// IsProcessedContext checks whether a key is currently marked as processed.
func (m *Manager) IsProcessedContext(ctx context.Context, key string) (bool, error) {
	cleanKey := strings.TrimSpace(key)
	if cleanKey == "" {
		return false, nil
	}
	if m.repo != nil {
		return m.repo.IsProcessed(ctx, cleanKey, time.Now().UTC())
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	e, exists := m.entries[cleanKey]
	return exists && time.Now().UTC().Before(e.expiresAt), nil
}

// IsProcessed is retained for compatibility. Hot paths should pass their context
// through IsProcessedContext instead.
func (m *Manager) IsProcessed(key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	processed, err := m.IsProcessedContext(ctx, key)
	return err == nil && processed
}

// SizeContext returns the current number of live idempotency keys.
func (m *Manager) SizeContext(ctx context.Context) (int, error) {
	if m.repo != nil {
		return m.repo.Size(ctx, time.Now().UTC())
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries), nil
}

// Size is retained for compatibility.
func (m *Manager) Size() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	size, err := m.SizeContext(ctx)
	if err != nil {
		return 0
	}
	return size
}
