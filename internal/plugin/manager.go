package plugin

import (
	"fmt"
	"strings"
	"sync"

	"github.com/inipew/goultroid/internal/core"
)

// Manager manages the lifecycle and registration of plugins.
type Manager struct {
	router  *core.Router
	plugins map[string]Plugin
	list    []Plugin
	mu      sync.RWMutex
}

// NewManager creates a new Manager wired to the given command Router.
func NewManager(router *core.Router) *Manager {
	return &Manager{
		router:  router,
		plugins: make(map[string]Plugin),
		list:    make([]Plugin, 0),
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

	m.plugins[name] = p
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

// Shutdown invokes Shutdown on all registered plugins that implement Shutdowner.
func (m *Manager) Shutdown() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []string
	for _, p := range m.list {
		if s, ok := p.(Shutdowner); ok {
			if err := s.Shutdown(); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", p.Name(), err))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors during plugin shutdown: %s", strings.Join(errs, "; "))
	}
	return nil
}
