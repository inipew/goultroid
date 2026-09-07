package menu

import (
	"fmt"
	"sync"
	"time"
)

// DefaultMenuTTL defines how long an interactive menu instance remains active.
const DefaultMenuTTL = 24 * time.Hour

// MenuInstance tracks the state and lifespan of an active menu message.
type MenuInstance struct {
	ID        string
	OwnerID   int64
	ChatID    int64
	MessageID int
	Screen    ScreenID
	CreatedAt time.Time
	ExpiresAt time.Time
}

// InstanceStore manages menu instances and expiration.
type InstanceStore interface {
	Register(instance MenuInstance)
	Get(chatID int64, messageID int) (*MenuInstance, bool)
	UpdateScreen(chatID int64, messageID int, screen ScreenID)
	Invalidate(chatID int64, messageID int)
	LockInstance(chatID int64, messageID int) func()
}

// MemoryInstanceStore provides an in-memory thread-safe instance store.
type MemoryInstanceStore struct {
	mu        sync.RWMutex
	instances map[string]*MenuInstance
	locks     map[string]*sync.Mutex
	ttl       time.Duration
}

var _ InstanceStore = (*MemoryInstanceStore)(nil)

// NewMemoryInstanceStore creates an initialized MemoryInstanceStore.
func NewMemoryInstanceStore(ttl time.Duration) *MemoryInstanceStore {
	if ttl <= 0 {
		ttl = DefaultMenuTTL
	}
	return &MemoryInstanceStore{
		instances: make(map[string]*MenuInstance),
		locks:     make(map[string]*sync.Mutex),
		ttl:       ttl,
	}
}

func instanceKey(chatID int64, messageID int) string {
	return fmt.Sprintf("%d:%d", chatID, messageID)
}

// Register adds or replaces a menu instance.
func (s *MemoryInstanceStore) Register(instance MenuInstance) {
	if instance.MessageID == 0 {
		return
	}
	now := time.Now()
	if instance.ID == "" {
		instance.ID = fmt.Sprintf("menu:%d:%d:%d", instance.ChatID, instance.MessageID, now.UnixNano())
	}
	if instance.CreatedAt.IsZero() {
		instance.CreatedAt = now
	}
	if instance.ExpiresAt.IsZero() {
		instance.ExpiresAt = now.Add(s.ttl)
	}

	key := instanceKey(instance.ChatID, instance.MessageID)
	s.mu.Lock()
	defer s.mu.Unlock()

	s.instances[key] = &instance

	// Periodic prune if store grows. Never remove lock entries here: a concurrent
	// callback may still hold a lock for this key. Replacing the mutex while it
	// is held would break the per-message serialization guarantee.
	if len(s.instances) > 500 {
		for k, inst := range s.instances {
			if now.After(inst.ExpiresAt) {
				delete(s.instances, k)
			}
		}
	}
}

// Get returns the instance if present and not expired.
func (s *MemoryInstanceStore) Get(chatID int64, messageID int) (*MenuInstance, bool) {
	key := instanceKey(chatID, messageID)
	s.mu.RLock()
	inst, ok := s.instances[key]
	s.mu.RUnlock()

	if !ok || inst == nil {
		return nil, false
	}

	if time.Now().After(inst.ExpiresAt) {
		s.mu.Lock()
		delete(s.instances, key)
		s.mu.Unlock()
		return nil, false
	}

	return inst, true
}

// UpdateScreen updates the active ScreenID of a registered instance.
func (s *MemoryInstanceStore) UpdateScreen(chatID int64, messageID int, screen ScreenID) {
	key := instanceKey(chatID, messageID)
	s.mu.Lock()
	defer s.mu.Unlock()

	if inst, ok := s.instances[key]; ok && inst != nil {
		inst.Screen = screen
	}
}

// Invalidate explicitly removes a menu instance (e.g. upon Close).
// The per-instance mutex is intentionally retained so a later callback cannot
// acquire a different mutex while an earlier callback is still finishing.
func (s *MemoryInstanceStore) Invalidate(chatID int64, messageID int) {
	key := instanceKey(chatID, messageID)
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.instances, key)
}

// LockInstance acquires a mutex dedicated to the specified menu instance
// and returns an unlock callback function to release the lock.
func (s *MemoryInstanceStore) LockInstance(chatID int64, messageID int) func() {
	key := instanceKey(chatID, messageID)
	s.mu.Lock()
	l, ok := s.locks[key]
	if !ok {
		l = &sync.Mutex{}
		s.locks[key] = l
	}
	s.mu.Unlock()

	l.Lock()
	return l.Unlock
}
