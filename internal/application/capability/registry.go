package capability

import (
	"fmt"
	"strings"
	"sync"

	"github.com/inipew/goultroid/internal/execution"
)

// Registry manages registered capabilities across all GoUltroid modules and plugins.
type Registry struct {
	mu           sync.RWMutex
	capabilities map[string]Capability
	ordered      []Capability
}

// NewRegistry creates an empty Capability Registry.
func NewRegistry() *Registry {
	return &Registry{
		capabilities: make(map[string]Capability),
		ordered:      make([]Capability, 0),
	}
}

// Register adds a single capability atomically.
func (r *Registry) Register(c Capability) error {
	return r.RegisterBatch([]Capability{c})
}

// RegisterBatch registers a slice of capabilities after validating there are no duplicates.
func (r *Registry) RegisterBatch(caps []Capability) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	pending := make(map[string]struct{})
	for _, c := range caps {
		id := strings.ToLower(strings.TrimSpace(c.ID))
		if id == "" {
			return fmt.Errorf("capability ID cannot be empty")
		}
		if _, exists := r.capabilities[id]; exists {
			return fmt.Errorf("capability already registered: %s", id)
		}
		if _, exists := pending[id]; exists {
			return fmt.Errorf("capability duplicated in batch: %s", id)
		}
		pending[id] = struct{}{}
	}

	for _, c := range caps {
		id := strings.ToLower(strings.TrimSpace(c.ID))
		c.ID = id
		r.capabilities[id] = c
		r.ordered = append(r.ordered, c)
	}
	return nil
}

// Find retrieves a capability by ID.
func (r *Registry) Find(id string) (Capability, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.capabilities[strings.ToLower(strings.TrimSpace(id))]
	return c, ok
}

// FindBySurface returns all capabilities supported on the specified execution source.
func (r *Registry) FindBySurface(source execution.Source) []Capability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return FilterBySurface(r.ordered, source)
}

// All returns a slice of all registered capabilities in insertion order.
func (r *Registry) All() []Capability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Capability, len(r.ordered))
	copy(result, r.ordered)
	return result
}
