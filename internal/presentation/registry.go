package presentation

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Registration holds the metadata and builder for a screen route.
type Registration struct {
	Owner      string
	Generation uint64
	Builder    Builder
	Policy     AccessPolicy
}

// Lease manages the lifecycle of a registered screen and allows deterministic cleanup.
type Lease struct {
	reg      Registration
	registry *Registry
	closed   atomic.Bool
}

// Close unregisters the screen lease if its generation matches the active registration.
func (l *Lease) Close() {
	if l == nil || !l.closed.CompareAndSwap(false, true) {
		return
	}
	if l.registry != nil {
		l.registry.unregister(l.reg)
	}
}

// Registry stores screen registrations with lease-based lifecycle and generation isolation.
type Registry struct {
	mu      sync.RWMutex
	screens map[ScreenKey]Registration
}

// NewRegistry creates a new empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		screens: make(map[ScreenKey]Registration),
	}
}

// Register registers a new Screen builder with associated policy and returns a cleanup Lease.
func (r *Registry) Register(reg Registration) (*Lease, error) {
	if reg.Builder == nil {
		return nil, ErrNilBuilder
	}
	key := reg.Builder.Key()
	if key.IsZero() {
		return nil, fmt.Errorf("%w: screen key cannot be empty", ErrInvalidPresentation)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, exists := r.screens[key]; exists {
		// Reject if active screen is owned by a different owner
		if existing.Owner != "" && existing.Owner != reg.Owner {
			return nil, fmt.Errorf("%w: screen %s is already registered by %q",
				ErrDuplicateScreenKey, key, existing.Owner)
		}
		// If same owner, reject unless new registration has a strictly higher generation
		if existing.Owner == reg.Owner && existing.Generation >= reg.Generation {
			return nil, fmt.Errorf("%w: cannot overwrite active generation %d with older or equal %d for %s",
				ErrDuplicateScreenKey, existing.Generation, reg.Generation, key)
		}
	}

	r.screens[key] = reg
	return &Lease{
		reg:      reg,
		registry: r,
	}, nil
}

// Resolve looks up the registration for the given ScreenKey.
func (r *Registry) Resolve(key ScreenKey) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	reg, ok := r.screens[key]
	return reg, ok
}

// unregister removes a registration if its identity matches the active entry.
func (r *Registry) unregister(target Registration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := target.Builder.Key()
	if current, exists := r.screens[key]; exists {
		if current.Owner == target.Owner && current.Generation == target.Generation {
			delete(r.screens, key)
		}
	}
}

// RevokeOwner removes all screen registrations owned by a specific plugin generation.
func (r *Registry) RevokeOwner(owner string, generation uint64) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	revoked := 0
	for key, reg := range r.screens {
		if reg.Owner == owner && (generation == 0 || reg.Generation == generation) {
			delete(r.screens, key)
			revoked++
		}
	}
	return revoked
}

// List returns an immutable snapshot of all active registrations.
func (r *Registry) List() []Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Registration, 0, len(r.screens))
	for _, reg := range r.screens {
		out = append(out, reg)
	}
	return out
}
