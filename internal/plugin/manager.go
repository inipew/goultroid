package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/inipew/goultroid/internal/core"
)

type Manager struct {
	router   *core.Router
	plugins  map[string]Plugin
	metadata map[string]Metadata
	list     []Plugin
	mu       sync.RWMutex
	shutdown bool
}

func NewManager(router *core.Router) *Manager {
	return &Manager{router: router, plugins: make(map[string]Plugin), metadata: make(map[string]Metadata), list: make([]Plugin, 0)}
}

func (m *Manager) Register(p Plugin) error {
	if p == nil { return fmt.Errorf("cannot register nil plugin") }
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shutdown { return fmt.Errorf("plugin manager is shutting down") }
	if m.router == nil { return fmt.Errorf("plugin router is nil") }
	name := strings.ToLower(strings.TrimSpace(p.Name()))
	if name == "" { return fmt.Errorf("plugin name cannot be empty") }
	if _, exists := m.plugins[name]; exists { return fmt.Errorf("plugin already registered: %s", name) }

	if err := p.Init(); err != nil {
		if s, ok := p.(ContextShutdowner); ok { _ = s.ShutdownContext(context.Background()) } else if s, ok := p.(Shutdowner); ok { _ = s.Shutdown() }
		return fmt.Errorf("failed to initialize plugin %s: %w", name, err)
	}
	if err := m.router.RegisterBatch(p.Commands()); err != nil {
		if s, ok := p.(ContextShutdowner); ok { _ = s.ShutdownContext(context.Background()) } else if s, ok := p.(Shutdowner); ok { _ = s.Shutdown() }
		return fmt.Errorf("plugin %s command registration failed: %w", name, err)
	}

	var meta Metadata
	if dp, ok := p.(DescribedPlugin); ok { meta = dp.Metadata() } else { meta = Metadata{Name: name, Version: "1.0.0", Description: fmt.Sprintf("%s plugin", name)} }
	if meta.Name == "" { meta.Name = name }
	if meta.Version == "" { meta.Version = "1.0.0" }
	m.plugins[name] = p
	m.metadata[name] = meta
	m.list = append(m.list, p)
	return nil
}

func (m *Manager) Plugins() []Plugin { m.mu.RLock(); defer m.mu.RUnlock(); res := make([]Plugin, len(m.list)); copy(res, m.list); return res }
func (m *Manager) Find(name string) (Plugin, bool) { m.mu.RLock(); defer m.mu.RUnlock(); p, ok := m.plugins[strings.ToLower(strings.TrimSpace(name))]; return p, ok }
func (m *Manager) GetMetadata(name string) (Metadata, bool) { m.mu.RLock(); defer m.mu.RUnlock(); meta, ok := m.metadata[strings.ToLower(strings.TrimSpace(name))]; return meta, ok }
func (m *Manager) AllMetadata() map[string]Metadata { m.mu.RLock(); defer m.mu.RUnlock(); res := make(map[string]Metadata, len(m.metadata)); for k, v := range m.metadata { res[k] = v }; return res }

func (m *Manager) Shutdown() error { return m.ShutdownWithContext(context.Background()) }

// ShutdownWithContext shuts plugins down in reverse registration order. A
// context-aware plugin receives the shutdown context. Legacy Shutdowner plugins
// are synchronous because their API cannot be cancelled; this prevents shared
// resources from being closed while legacy teardown is still running.
func (m *Manager) ShutdownWithContext(ctx context.Context) error {
	if ctx == nil { ctx = context.Background() }
	m.mu.Lock()
	if m.shutdown { m.mu.Unlock(); return nil }
	m.shutdown = true
	plugins := make([]Plugin, len(m.list)); copy(plugins, m.list)
	m.mu.Unlock()

	var errs []string
	for i := len(plugins) - 1; i >= 0; i-- {
		p := plugins[i]
		select {
		case <-ctx.Done():
			errs = append(errs, fmt.Sprintf("shutdown context cancelled before %s: %v", p.Name(), ctx.Err()))
			return fmt.Errorf("errors during plugin shutdown: %s", strings.Join(errs, "; "))
		default:
		}
		var err error
		if s, ok := p.(ContextShutdowner); ok { err = s.ShutdownContext(ctx) } else if s, ok := p.(Shutdowner); ok { err = s.Shutdown() }
		if err != nil { errs = append(errs, fmt.Sprintf("%s: %v", p.Name(), err)) }
	}
	if len(errs) > 0 { return fmt.Errorf("errors during plugin shutdown: %s", strings.Join(errs, "; ")) }
	return nil
}
