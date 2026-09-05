package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/inipew/goultroid/internal/core"
)

// Manager manages the lifecycle and registration of plugins.
type Manager struct {
	router   *core.Router
	plugins  map[string]Plugin
	metadata map[string]Metadata
	list     []Plugin
	mu       sync.RWMutex
}

// NewManager creates a new Manager wired to the given command Router.
func NewManager(router *core.Router) *Manager {
	return &Manager{
		router:   router,
		plugins:  make(map[string]Plugin),
		metadata: make(map[string]Metadata),
		list:     make([]Plugin, 0),
	}
}

// Register initializes a plugin and registers all its commands into the router.
func (m *Manager) Register(p Plugin) error {
	if p == nil {
		return fmt.Errorf("cannot register nil plugin")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	name := strings.ToLower(strings.TrimSpace(p.Name()))
	if name == "" {
		return fmt.Errorf("plugin name cannot be empty")
	}

	if _, exists := m.plugins[name]; exists {
		return fmt.Errorf("plugin already registered: %s", name)
	}

	if err := p.Init(); err != nil {
		return fmt.Errorf("failed to initialize plugin %s: %w", name, err)
	}

	for _, cmd := range p.Commands() {
		if err := m.router.Register(cmd); err != nil {
			return fmt.Errorf("plugin %s command registration failed: %w", name, err)
		}
	}

	var meta Metadata
	if dp, ok := p.(DescribedPlugin); ok {
		meta = dp.Metadata()
	} else {
		meta = Metadata{
			Name:        name,
			Version:     "1.0.0",
			Description: fmt.Sprintf("%s plugin", name),
		}
	}
	if meta.Name == "" {
		meta.Name = name
	}
	if meta.Version == "" {
		meta.Version = "1.0.0"
	}

	m.plugins[name] = p
	m.metadata[name] = meta
	m.list = append(m.list, p)
	return nil
}

// Plugins returns a copy of the registered plugins slice.
func (m *Manager) Plugins() []Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make([]Plugin, len(m.list))
	copy(res, m.list)
	return res
}

// Find retrieves a registered plugin by name.
func (m *Manager) Find(name string) (Plugin, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	p, ok := m.plugins[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// GetMetadata retrieves metadata for a registered plugin.
func (m *Manager) GetMetadata(name string) (Metadata, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	meta, ok := m.metadata[strings.ToLower(strings.TrimSpace(name))]
	return meta, ok
}

// AllMetadata returns a copy of all registered plugin metadata.
func (m *Manager) AllMetadata() map[string]Metadata {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make(map[string]Metadata, len(m.metadata))
	for k, v := range m.metadata {
		res[k] = v
	}
	return res
}

// Shutdown invokes Shutdown on all registered plugins that implement Shutdowner.
func (m *Manager) Shutdown() error {
	return m.ShutdownWithContext(context.Background())
}

// ShutdownWithContext invokes Shutdown on all plugins respecting context budget.
// If context expires, remaining plugins are skipped and context error is returned.
func (m *Manager) ShutdownWithContext(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []string
	for _, p := range m.list {
		select {
		case <-ctx.Done():
			errs = append(errs, fmt.Sprintf("shutdown context cancelled before %s: %v", p.Name(), ctx.Err()))
			return fmt.Errorf("errors during plugin shutdown: %s", strings.Join(errs, "; "))
		default:
		}
		if s, ok := p.(Shutdowner); ok {
			// Run shutdown with context awareness: abort if ctx done
			done := make(chan error, 1)
			go func(sh Shutdowner) { done <- sh.Shutdown() }(s)
			select {
			case <-ctx.Done():
				errs = append(errs, fmt.Sprintf("%s: shutdown timed out: %v", p.Name(), ctx.Err()))
			case err := <-done:
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", p.Name(), err))
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors during plugin shutdown: %s", strings.Join(errs, "; "))
	}
	return nil
}
