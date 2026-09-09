package resource

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type LeakPolicy string

const (
	LeakPolicyWarn         LeakPolicy = "warn"
	LeakPolicyDegraded     LeakPolicy = "degraded"
	LeakPolicyForceCleanup LeakPolicy = "force_cleanup"
)

// Manager tracks, inspects, and audits the ownership and lifecycle of long-lived resources.
type Manager struct {
	mu        sync.RWMutex
	policy    LeakPolicy
	resources map[string]Resource
	cleanups  map[string]func() error
}

// NewManager initializes a new thread-safe resource manager.
func NewManager() *Manager {
	return &Manager{
		policy:    LeakPolicyWarn,
		resources: make(map[string]Resource),
		cleanups:  make(map[string]func() error),
	}
}

// SetLeakPolicy sets the policy applied when leaks are detected.
func (m *Manager) SetLeakPolicy(p LeakPolicy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policy = p
}

// LeakPolicy returns the active leak policy.
func (m *Manager) LeakPolicy() LeakPolicy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.policy
}

// Register adds a resource to tracking. Every resource must have an ID, Owner, and Type.
func (m *Manager) Register(r Resource) error {
	return m.RegisterWithCleanup(r, nil)
}

// RegisterWithCleanup adds a resource to tracking along with an optional force-cleanup function.
func (m *Manager) RegisterWithCleanup(r Resource, cleanup func() error) error {
	if strings.TrimSpace(r.ID) == "" {
		return errors.New("resource ID cannot be empty")
	}
	if strings.TrimSpace(r.Owner) == "" {
		return errors.New("resource Owner cannot be empty")
	}
	if strings.TrimSpace(r.Type) == "" {
		return errors.New("resource Type cannot be empty")
	}

	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if r.State == "" {
		r.State = StateActive
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.resources[r.ID]; exists {
		return fmt.Errorf("resource with ID %q already registered", r.ID)
	}

	m.resources[r.ID] = r
	if cleanup != nil {
		m.cleanups[r.ID] = cleanup
	}
	return nil
}

// Release marks a resource as released and removes it from active tracking.
func (m *Manager) Release(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, exists := m.resources[id]
	if !exists {
		return fmt.Errorf("resource with ID %q not found", id)
	}

	r.State = StateReleased
	delete(m.resources, id)
	delete(m.cleanups, id)
	return nil
}

// Get returns the resource by ID, if present.
func (m *Manager) Get(id string) (Resource, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, exists := m.resources[id]
	return r, exists
}

// ByOwner returns all currently tracked active resources for a given owner.
func (m *Manager) ByOwner(owner string) []Resource {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []Resource
	for _, r := range m.resources {
		if r.Owner == owner {
			result = append(result, r)
		}
	}
	return result
}

// ByType returns all currently tracked active resources of a given type.
func (m *Manager) ByType(rType string) []Resource {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []Resource
	for _, r := range m.resources {
		if r.Type == rType {
			result = append(result, r)
		}
	}
	return result
}

// All returns a slice of all currently tracked active resources.
func (m *Manager) All() []Resource {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Resource, 0, len(m.resources))
	for _, r := range m.resources {
		result = append(result, r)
	}
	return result
}

// DetectLeaks checks if there are any remaining active resources for an owner that is
// expected to be stopped. It marks those resources as StateLeaked.
func (m *Manager) DetectLeaks(owner string) []Resource {
	m.mu.Lock()
	defer m.mu.Unlock()

	var leaks []Resource
	for id, r := range m.resources {
		if r.Owner == owner && r.State == StateActive {
			r.State = StateLeaked
			m.resources[id] = r
			leaks = append(leaks, r)
		}
	}
	return leaks
}

// OwnerSnapshot returns an aggregated view of active resources for the specified owner.
func (m *Manager) OwnerSnapshot(owner string) OwnerSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := OwnerSnapshot{
		Owner:        owner,
		CountsByType: make(map[string]int),
	}

	for _, r := range m.resources {
		if r.Owner == owner {
			snapshot.TotalActive++
			snapshot.CountsByType[r.Type]++
			if r.State == StateLeaked {
				snapshot.Leaked++
			}
		}
	}
	return snapshot
}

// AllSnapshots returns aggregated views for all owners with active resources.
func (m *Manager) AllSnapshots() []OwnerSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	byOwner := make(map[string]*OwnerSnapshot)
	for _, r := range m.resources {
		snap, ok := byOwner[r.Owner]
		if !ok {
			snap = &OwnerSnapshot{
				Owner:        r.Owner,
				CountsByType: make(map[string]int),
			}
			byOwner[r.Owner] = snap
		}
		snap.TotalActive++
		snap.CountsByType[r.Type]++
		if r.State == StateLeaked {
			snap.Leaked++
		}
	}

	result := make([]OwnerSnapshot, 0, len(byOwner))
	for _, s := range byOwner {
		result = append(result, *s)
	}
	return result
}

// ForceCleanupOwner executes cleanup functions for all tracked resources belonging to owner,
// releases them, and returns any cleanup errors encountered.
func (m *Manager) ForceCleanupOwner(owner string) []error {
	m.mu.Lock()
	var toClean []struct {
		id      string
		cleanup func() error
	}
	for id, r := range m.resources {
		if r.Owner == owner {
			cleanup := m.cleanups[id]
			toClean = append(toClean, struct {
				id      string
				cleanup func() error
			}{id: id, cleanup: cleanup})
			delete(m.resources, id)
			delete(m.cleanups, id)
		}
	}
	m.mu.Unlock()

	var errs []error
	for _, item := range toClean {
		if item.cleanup != nil {
			if err := item.cleanup(); err != nil {
				errs = append(errs, fmt.Errorf("cleanup %s: %w", item.id, err))
			}
		}
	}
	return errs
}

// HandleLeaks checks if owner has active resources, marks them leaked, and applies the LeakPolicy.
func (m *Manager) HandleLeaks(owner string) ([]Resource, error) {
	leaks := m.DetectLeaks(owner)
	if len(leaks) == 0 {
		return nil, nil
	}

	policy := m.LeakPolicy()
	switch policy {
	case LeakPolicyForceCleanup:
		errs := m.ForceCleanupOwner(owner)
		if len(errs) > 0 {
			return leaks, fmt.Errorf("force cleanup encountered errors: %w", errors.Join(errs...))
		}
		return leaks, nil
	case LeakPolicyDegraded:
		return leaks, fmt.Errorf("resource leaks detected under degraded policy: %d resources", len(leaks))
	default: // LeakPolicyWarn
		return leaks, nil
	}
}
