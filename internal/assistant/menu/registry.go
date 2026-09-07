package menu

import (
	"fmt"
	"sync"
)

// ScreenBuilder is a function that produces a dynamic Screen.
type ScreenBuilder func(ctx any) (*Screen, error)

// Registry manages registered screen builders.
type Registry struct {
	mu      sync.RWMutex
	screens map[ScreenID]ScreenBuilder
}

// NewRegistry creates an empty Screen Registry.
func NewRegistry() *Registry {
	return &Registry{
		screens: make(map[ScreenID]ScreenBuilder),
	}
}

// Register attaches a builder to a ScreenID.
func (r *Registry) Register(id ScreenID, builder ScreenBuilder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.screens[id] = builder
}

// Get retrieves a builder for a ScreenID.
func (r *Registry) Get(id ScreenID) (ScreenBuilder, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.screens[id]
	return b, ok
}

// Build creates a Screen from a registered builder.
func (r *Registry) Build(id ScreenID, ctx any) (*Screen, error) {
	builder, ok := r.Get(id)
	if !ok {
		return nil, fmt.Errorf("assistant/menu: screen %q not registered", id)
	}
	return builder(ctx)
}
