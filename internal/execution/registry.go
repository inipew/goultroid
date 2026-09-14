package execution

import (
	"errors"
	"fmt"
	"sync"

	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

type handlerKey struct {
	name    string
	version uint16
}

// Registry is the executable-capability side of the immutable HandlerRef model.
// Metadata lives in TaskEngine's Catalog; closures live only in this in-memory,
// instance-owned table and are never copied into WorkSpec or durable Job values.
type Registry struct {
	catalog *taskengine.Catalog
	mu       sync.RWMutex
	handlers map[handlerKey]tasks.HandlerFunc
}

func NewRegistry(catalog *taskengine.Catalog) (*Registry, error) {
	if catalog == nil {
		return nil, errors.New("execution catalog is required")
	}
	return &Registry{catalog: catalog, handlers: make(map[handlerKey]tasks.HandlerFunc)}, nil
}

func (r *Registry) Register(descriptor taskengine.HandlerDescriptor, handler tasks.HandlerFunc) error {
	if handler == nil {
		return errors.New("handler implementation is required")
	}
	if err := r.catalog.RegisterHandler(descriptor); err != nil {
		return err
	}
	key := handlerKey{name: descriptor.Ref.Name(), version: descriptor.Ref.Version()}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[key]; exists {
		return fmt.Errorf("execution handler already registered: %s@%d", descriptor.Ref.Name(), descriptor.Ref.Version())
	}
	r.handlers[key] = handler
	return nil
}

func (r *Registry) ResolveHandler(ref tasks.HandlerRef) (tasks.HandlerFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	handler, ok := r.handlers[handlerKey{name: ref.Name(), version: ref.Version()}]
	return handler, ok
}
