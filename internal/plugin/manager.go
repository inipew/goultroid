package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// MessageHookHandler represents the raw Telegram message interceptor signature.
type MessageHookHandler = func(ctx context.Context, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) error

// HookRegistrar allows the plugin manager to attach message hooks into the dispatcher.
type HookRegistrar interface {
	AddPrioritizedMessageHandler(priority int, h MessageHookHandler) func()
}

type Manager struct {
	router        *core.Router
	hookRegistrar HookRegistrar
	plugins       map[string]Plugin
	metadata      map[string]Metadata
	list          []Plugin
	hookCleanups  []func()
	mu            sync.RWMutex
	shutdown      bool
}

func NewManager(router *core.Router) *Manager {
	return &Manager{router: router, plugins: make(map[string]Plugin), metadata: make(map[string]Metadata), list: make([]Plugin, 0)}
}

// SetHookRegistrar attaches a hook registrar (e.g. Telegram Dispatcher) to this manager.
func (m *Manager) SetHookRegistrar(registrar HookRegistrar) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hookRegistrar = registrar
}

func (m *Manager) Register(p Plugin) error {
	return m.RegisterWithContext(context.Background(), p)
}

// RegisterWithContext registers and initializes a plugin using the provided startup context.
func (m *Manager) RegisterWithContext(ctx context.Context, p Plugin) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if p == nil { return fmt.Errorf("cannot register nil plugin") }
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shutdown { return fmt.Errorf("plugin manager is shutting down") }
	if m.router == nil { return fmt.Errorf("plugin router is nil") }
	name := strings.ToLower(strings.TrimSpace(p.Name()))
	if name == "" { return fmt.Errorf("plugin name cannot be empty") }
	if _, exists := m.plugins[name]; exists { return fmt.Errorf("plugin already registered: %s", name) }

	// 1. Startup validation: ensure commands are valid and handlers are non-nil
	cmds := p.Commands()
	for _, cmd := range cmds {
		if strings.TrimSpace(cmd.Name) == "" {
			return fmt.Errorf("plugin %s contains command with empty name", name)
		}
		if cmd.Handler == nil {
			return fmt.Errorf("plugin %s command %q has nil handler", name, cmd.Name)
		}
	}

	// 2. Context-aware initialization
	var initErr error
	if ci, ok := p.(ContextInitializer); ok {
		initErr = ci.InitContext(ctx)
	} else {
		initErr = p.Init()
	}
	if initErr != nil {
		if s, ok := p.(ContextShutdowner); ok { _ = s.ShutdownContext(ctx) } else if s, ok := p.(Shutdowner); ok { _ = s.Shutdown() }
		return fmt.Errorf("failed to initialize plugin %s: %w", name, initErr)
	}

	// 3. Command registration
	if err := m.router.RegisterBatch(cmds); err != nil {
		if s, ok := p.(ContextShutdowner); ok { _ = s.ShutdownContext(ctx) } else if s, ok := p.(Shutdowner); ok { _ = s.Shutdown() }
		return fmt.Errorf("plugin %s command registration failed: %w", name, err)
	}

	// 4. Hook registration for plugins that intercept raw Telegram messages
	if m.hookRegistrar != nil {
		if mhp, ok := p.(MessageHookPlugin); ok {
			cleanup := m.hookRegistrar.AddPrioritizedMessageHandler(mhp.MessageHookPriority(), mhp.HandleIncomingMessage)
			if cleanup != nil {
				m.hookCleanups = append(m.hookCleanups, cleanup)
			}
		}
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
// resources from being closed while legacy teardown is still running. Context
// cancellation does not skip later plugins: every registered plugin gets one
// shutdown attempt so a database or network resource is not left running.
func (m *Manager) ShutdownWithContext(ctx context.Context) error {
	if ctx == nil { ctx = context.Background() }
	m.mu.Lock()
	if m.shutdown { m.mu.Unlock(); return nil }
	m.shutdown = true
	cleanups := m.hookCleanups
	m.hookCleanups = nil
	plugins := make([]Plugin, len(m.list)); copy(plugins, m.list)
	m.mu.Unlock()

	// 1. Detach all message hooks first so no incoming update hits shutting-down plugins
	for _, cleanup := range cleanups {
		if cleanup != nil {
			cleanup()
		}
	}

	// 2. Shut down plugins in reverse registration order
	var errs []string
	for i := len(plugins) - 1; i >= 0; i-- {
		p := plugins[i]
		var err error
		if s, ok := p.(ContextShutdowner); ok { err = s.ShutdownContext(ctx) } else if s, ok := p.(Shutdowner); ok { err = s.Shutdown() }
		if err != nil { errs = append(errs, fmt.Sprintf("%s: %v", p.Name(), err)) }
	}
	if len(errs) > 0 { return fmt.Errorf("errors during plugin shutdown: %s", strings.Join(errs, "; ")) }
	return nil
}
