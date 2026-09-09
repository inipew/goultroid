package plugin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/resource"
)

// MessageHookHandler represents the raw Telegram message interceptor signature.
type MessageHookHandler = func(ctx context.Context, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) error

// HookRegistrar allows the plugin manager to attach message hooks into the dispatcher.
type HookRegistrar interface {
	AddPrioritizedMessageHandler(priority int, h MessageHookHandler) func()
}

type Manager struct {
	router          *core.Router
	hookRegistrar   HookRegistrar
	resourceManager *resource.Manager
	plugins         map[string]Plugin
	metadata        map[string]Metadata
	scopes          map[string]*Scope
	commands        map[string][]core.Command
	disabled        map[string]bool
	list            []Plugin
	hookCleanups    []func()
	mu              sync.RWMutex
	shutdown        bool
}

func NewManager(router *core.Router) *Manager {
	return &Manager{
		router:   router,
		plugins:  make(map[string]Plugin),
		metadata: make(map[string]Metadata),
		scopes:   make(map[string]*Scope),
		commands: make(map[string][]core.Command),
		disabled: make(map[string]bool),
		list:     make([]Plugin, 0),
	}
}

// SetResourceManager sets the global resource manager used to track plugin scopes.
func (m *Manager) SetResourceManager(mgr *resource.Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resourceManager = mgr
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
// It is transactional: no partial registration is visible on failure, and manager mutex is never held while invoking plugin code.
func (m *Manager) RegisterWithContext(ctx context.Context, p Plugin) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if p == nil {
		return fmt.Errorf("cannot register nil plugin")
	}
	name := strings.ToLower(strings.TrimSpace(p.Name()))
	if name == "" {
		return fmt.Errorf("plugin name cannot be empty")
	}

	// 1. Lightweight pre-check under lock (duplicate, shutdown, router nil)
	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return fmt.Errorf("plugin manager is shutting down")
	}
	if m.router == nil {
		m.mu.Unlock()
		return fmt.Errorf("plugin router is nil")
	}
	if _, exists := m.plugins[name]; exists {
		m.mu.Unlock()
		return fmt.Errorf("plugin already registered: %s", name)
	}
	// Copy router reference for validate without holding lock
	router := m.router
	m.mu.Unlock()

	// 2. Validate commands outside lock
	cmds := p.Commands()
	for _, cmd := range cmds {
		if strings.TrimSpace(cmd.Name) == "" {
			return fmt.Errorf("plugin %s contains command with empty name", name)
		}
		if cmd.Handler == nil {
			return fmt.Errorf("plugin %s command %q has nil handler", name, cmd.Name)
		}
	}

	// 3. Initialization without holding lock (may be long, may call manager)
	m.mu.RLock()
	resMgr := m.resourceManager
	m.mu.RUnlock()

	var scope *Scope
	if resMgr != nil {
		scope = NewScopeWithManager(ctx, "plugin:"+name, resMgr)
	} else {
		scope = NewScope(ctx, "plugin:"+name)
	}
	var initErr error
	if si, ok := p.(ScopeInitializer); ok {
		initErr = si.InitScope(scope.Context(), scope)
	} else if ci, ok := p.(ContextInitializer); ok {
		initErr = ci.InitContext(ctx)
	} else {
		initErr = p.Init()
	}
	if initErr != nil {
		// Compensating cleanup outside lock
		if s, ok := p.(ContextShutdowner); ok {
			_ = s.ShutdownContext(ctx)
		} else if s, ok := p.(Shutdowner); ok {
			_ = s.Shutdown()
		}
		_ = scope.Close(ctx)
		return fmt.Errorf("failed to initialize plugin %s: %w", name, initErr)
	}

	// 4. Atomic commit under lock: re-check, register commands, hooks, metadata
	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		_ = scope.Close(ctx)
		if s, ok := p.(ContextShutdowner); ok {
			_ = s.ShutdownContext(ctx)
		} else if s, ok := p.(Shutdowner); ok {
			_ = s.Shutdown()
		}
		return fmt.Errorf("plugin manager is shutting down")
	}
	if _, exists := m.plugins[name]; exists {
		m.mu.Unlock()
		_ = scope.Close(ctx)
		if s, ok := p.(ContextShutdowner); ok {
			_ = s.ShutdownContext(ctx)
		} else if s, ok := p.(Shutdowner); ok {
			_ = s.Shutdown()
		}
		return fmt.Errorf("plugin already registered: %s", name)
	}
	if err := router.RegisterBatch(cmds); err != nil {
		m.mu.Unlock()
		_ = scope.Close(ctx)
		if s, ok := p.(ContextShutdowner); ok {
			_ = s.ShutdownContext(ctx)
		} else if s, ok := p.(Shutdowner); ok {
			_ = s.Shutdown()
		}
		return fmt.Errorf("plugin %s command registration failed: %w", name, err)
	}

	var hookCleanup func()
	if m.hookRegistrar != nil {
		if mhp, ok := p.(MessageHookPlugin); ok {
			hookCleanup = m.hookRegistrar.AddPrioritizedMessageHandler(mhp.MessageHookPriority(), mhp.HandleIncomingMessage)
		}
	}

	var meta Metadata
	if dp, ok := p.(DescribedPlugin); ok {
		meta = dp.Metadata()
	} else {
		meta = Metadata{Name: name, Version: "1.0.0", Description: fmt.Sprintf("%s plugin", name)}
	}
	if meta.Name == "" {
		meta.Name = name
	}
	if meta.Version == "" {
		meta.Version = "1.0.0"
	}
	m.plugins[name] = p
	m.metadata[name] = meta
	m.scopes[name] = scope
	m.commands[name] = append([]core.Command(nil), cmds...)
	m.list = append(m.list, p)
	if hookCleanup != nil {
		m.hookCleanups = append(m.hookCleanups, hookCleanup)
	}
	m.mu.Unlock()
	return nil
}

// Scope returns the lifecycle scope owned by a registered plugin.
func (m *Manager) Scope(name string) (*Scope, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	scope, ok := m.scopes[strings.ToLower(strings.TrimSpace(name))]
	return scope, ok
}

func (m *Manager) Plugins() []Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]Plugin, len(m.list))
	copy(res, m.list)
	return res
}
func (m *Manager) Find(name string) (Plugin, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.plugins[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}
func (m *Manager) GetMetadata(name string) (Metadata, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	meta, ok := m.metadata[strings.ToLower(strings.TrimSpace(name))]
	return meta, ok
}
func (m *Manager) AllMetadata() map[string]Metadata {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make(map[string]Metadata, len(m.metadata))
	for k, v := range m.metadata {
		res[k] = v
	}
	return res
}

func (m *Manager) Shutdown() error { return m.ShutdownWithContext(context.Background()) }

// ShutdownWithContext shuts plugins down in reverse registration order. A
// context-aware plugin receives the shutdown context. Legacy Shutdowner plugins
// are synchronous because their API cannot be cancelled; this prevents shared
// resources from being closed while legacy teardown is still running. Context
// cancellation does not skip later plugins: every registered plugin gets one
// shutdown attempt so a database or network resource is not left running.
func (m *Manager) ShutdownWithContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return nil
	}
	m.shutdown = true
	cleanups := m.hookCleanups
	m.hookCleanups = nil
	plugins := make([]Plugin, len(m.list))
	copy(plugins, m.list)
	scopes := make(map[string]*Scope, len(m.scopes))
	commands := make(map[string][]core.Command, len(m.commands))
	for name, scope := range m.scopes {
		scopes[name] = scope
	}
	for name, cmds := range m.commands {
		commands[name] = append([]core.Command(nil), cmds...)
	}
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
		if s, ok := p.(ContextShutdowner); ok {
			err = s.ShutdownContext(ctx)
		} else if s, ok := p.(Shutdowner); ok {
			err = s.Shutdown()
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p.Name(), err))
		}
		if scope := scopes[strings.ToLower(strings.TrimSpace(p.Name()))]; scope != nil {
			if err := scope.Close(ctx); err != nil {
				errs = append(errs, fmt.Sprintf("%s scope: %v", p.Name(), err))
			}
		}
		m.router.UnregisterBatch(commands[strings.ToLower(strings.TrimSpace(p.Name()))])
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors during plugin shutdown: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Disable unregisters commands, cancels work, and executes shutdown hooks for a single plugin.
func (m *Manager) Disable(ctx context.Context, name string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	key := strings.ToLower(strings.TrimSpace(name))

	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return fmt.Errorf("plugin manager is shut down")
	}
	p, exists := m.plugins[key]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q not found", name)
	}
	if m.disabled[key] {
		m.mu.Unlock()
		return nil // already disabled
	}
	cmds := append([]core.Command(nil), m.commands[key]...)
	scope := m.scopes[key]
	delete(m.scopes, key)
	m.disabled[key] = true
	router := m.router
	m.mu.Unlock()

	// Unregister commands from router
	if router != nil && len(cmds) > 0 {
		router.UnregisterBatch(cmds)
	}

	// Close scope and execute shutdown hooks
	var errs []error
	if s, ok := p.(ContextShutdowner); ok {
		if err := s.ShutdownContext(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown %s: %w", name, err))
		}
	} else if s, ok := p.(Shutdowner); ok {
		if err := s.Shutdown(); err != nil {
			errs = append(errs, fmt.Errorf("shutdown %s: %w", name, err))
		}
	}

	if scope != nil {
		if err := scope.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close scope %s: %w", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors disabling plugin %s: %v", name, errs)
	}
	return nil
}

// Enable re-initializes and registers a previously disabled plugin.
func (m *Manager) Enable(ctx context.Context, name string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	key := strings.ToLower(strings.TrimSpace(name))

	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return fmt.Errorf("plugin manager is shut down")
	}
	p, exists := m.plugins[key]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q not found", name)
	}
	if !m.disabled[key] {
		m.mu.Unlock()
		return nil // already enabled
	}
	cmds := append([]core.Command(nil), m.commands[key]...)
	router := m.router
	resMgr := m.resourceManager
	m.mu.Unlock()

	// Initialize scope and plugin
	var scope *Scope
	if resMgr != nil {
		scope = NewScopeWithManager(ctx, "plugin:"+key, resMgr)
	} else {
		scope = NewScope(ctx, "plugin:"+key)
	}

	var initErr error
	if si, ok := p.(ScopeInitializer); ok {
		initErr = si.InitScope(scope.Context(), scope)
	} else if ci, ok := p.(ContextInitializer); ok {
		initErr = ci.InitContext(ctx)
	} else {
		initErr = p.Init()
	}

	if initErr != nil {
		_ = scope.Close(ctx)
		return fmt.Errorf("failed to re-initialize plugin %s: %w", name, initErr)
	}

	// Register commands back to router
	if router != nil && len(cmds) > 0 {
		if err := router.RegisterBatch(cmds); err != nil {
			_ = scope.Close(ctx)
			return fmt.Errorf("failed to re-register commands for plugin %s: %w", name, err)
		}
	}

	m.mu.Lock()
	m.scopes[key] = scope
	delete(m.disabled, key)
	m.mu.Unlock()

	return nil
}

// IsEnabled reports whether the given plugin is currently enabled.
func (m *Manager) IsEnabled(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := strings.ToLower(strings.TrimSpace(name))
	_, exists := m.plugins[key]
	return exists && !m.disabled[key]
}

// DisabledPlugins returns a list of names of currently disabled plugins.
func (m *Manager) DisabledPlugins() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var list []string
	for k, v := range m.disabled {
		if v {
			list = append(list, k)
		}
	}
	sort.Strings(list)
	return list
}
