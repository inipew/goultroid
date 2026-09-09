package plugin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/platform/audit"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/platform/process"
	"github.com/inipew/goultroid/internal/platform/secret"
	"github.com/inipew/goultroid/internal/platform/storage"
	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
)

// ManifestPlugin is an optional interface plugins can implement to declare their manifest directly.
type ManifestPlugin interface {
	Manifest() Manifest
}

// MessageHookHandler represents the raw Telegram message interceptor signature.
type MessageHookHandler = func(ctx context.Context, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) error

// HookRegistrar allows the plugin manager to attach message hooks into the dispatcher.
type HookRegistrar interface {
	AddPrioritizedMessageHandler(priority int, h MessageHookHandler) func()
}

// SchedulerTaskCleaner allows the plugin manager to unregister periodic tasks owned by disabled plugins.
type SchedulerTaskCleaner interface {
	UnregisterPeriodicTasksByOwner(owner string) int
}

type Manager struct {
	router            *core.Router
	hookRegistrar     HookRegistrar
	schedCleaner      SchedulerTaskCleaner
	resourceManager   *resource.Manager
	gate              *CapabilityGate
	networkService    *network.Service
	processManager    *process.Manager
	filesystemManager *filesystem.Manager
	secretManager     *secret.Manager
	taskManager       *tasks.Manager
	jobsManager       *jobs.Manager
	storageManager    *storage.Manager
	plugins           map[string]Plugin
	metadata          map[string]Metadata
	manifests         map[string]Manifest
	scopes            map[string]*Scope
	commands          map[string][]core.Command
	disabled          map[string]bool
	list              []Plugin
	hookCleanups      []func()
	auditor           audit.Auditor
	mu                sync.RWMutex
	shutdown          bool
}

func NewManager(router *core.Router) *Manager {
	return &Manager{
		router:    router,
		plugins:   make(map[string]Plugin),
		metadata:  make(map[string]Metadata),
		manifests: make(map[string]Manifest),
		scopes:    make(map[string]*Scope),
		commands:  make(map[string][]core.Command),
		disabled:  make(map[string]bool),
		list:      make([]Plugin, 0),
	}
}

// SetPlatformServices attaches platform managers and capability gate to this manager.
func (m *Manager) SetPlatformServices(
	gate *CapabilityGate,
	net *network.Service,
	proc *process.Manager,
	fs *filesystem.Manager,
	sec *secret.Manager,
	tsk *tasks.Manager,
	jbs *jobs.Manager,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gate = gate
	m.networkService = net
	m.processManager = proc
	m.filesystemManager = fs
	m.secretManager = sec
	m.taskManager = tsk
	m.jobsManager = jbs
}

// Gate returns the capability gate if configured.
func (m *Manager) Gate() *CapabilityGate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.gate
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

// SetSchedulerCleaner attaches a scheduler task cleaner to this manager.
func (m *Manager) SetSchedulerCleaner(cleaner SchedulerTaskCleaner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schedCleaner = cleaner
}

// SetAuditor attaches an audit logger to record plugin lifecycle events.
func (m *Manager) SetAuditor(a audit.Auditor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auditor = a
}

// SetStorageManager attaches a storage manager to this plugin manager.
func (m *Manager) SetStorageManager(mgr *storage.Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.storageManager = mgr
}

func (m *Manager) buildPluginContext(baseCtx context.Context, name string, scope *Scope) PluginContext {
	m.mu.RLock()
	gate := m.gate
	netSvc := m.networkService
	procMgr := m.processManager
	fsMgr := m.filesystemManager
	secMgr := m.secretManager
	taskMgr := m.taskManager
	jobsMgr := m.jobsManager
	storageMgr := m.storageManager
	m.mu.RUnlock()

	return NewPluginContext(baseCtx, ContextConfig{
		Scope:   scope,
		Owner:   name,
		Gate:    gate,
		Network: netSvc,
		Process: procMgr,
		Files:   fsMgr,
		Secrets: secMgr,
		Tasks:   taskMgr,
		Jobs:    jobsMgr,
		Storage: storageMgr,
	})
}

// RegisterModule registers a plugin with its formal manifest declarations.
func (m *Manager) RegisterModule(ctx context.Context, manifest Manifest, p Plugin) error {
	if p == nil {
		return fmt.Errorf("cannot register nil plugin")
	}
	name := strings.ToLower(strings.TrimSpace(p.Name()))
	if manifest.ID == "" {
		manifest.ID = name
	}
	if manifest.Name == "" {
		manifest.Name = p.Name()
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("invalid manifest for plugin %s: %w", name, err)
	}

	m.mu.Lock()
	m.manifests[name] = manifest
	gate := m.gate
	m.mu.Unlock()

	if gate != nil {
		if err := gate.RegisterManifest(manifest); err != nil {
			return fmt.Errorf("gate rejected manifest for plugin %s: %w", name, err)
		}
	}

	return m.RegisterWithContext(ctx, p)
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

	// Register manifest capabilities if implemented directly on the plugin
	if mp, ok := p.(ManifestPlugin); ok {
		manifest := mp.Manifest()
		if manifest.ID == "" {
			manifest.ID = name
		}
		if err := manifest.Validate(); err != nil {
			return fmt.Errorf("invalid manifest for plugin %s: %w", name, err)
		}
		m.mu.Lock()
		m.manifests[name] = manifest
		gate := m.gate
		m.mu.Unlock()
		if gate != nil {
			if err := gate.RegisterManifest(manifest); err != nil {
				return fmt.Errorf("gate rejected manifest for plugin %s: %w", name, err)
			}
		}
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
	pctx := m.buildPluginContext(scope.Context(), name, scope)

	var initErr error
	if pci, ok := p.(PluginContextInitializer); ok {
		initErr = pci.InitPlugin(pctx)
	} else if si, ok := p.(ScopeInitializer); ok {
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

// Manifest returns the declared manifest for the given plugin if registered.
func (m *Manager) Manifest(name string) (Manifest, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	manifest, ok := m.manifests[strings.ToLower(strings.TrimSpace(name))]
	return manifest, ok
}

// AllManifests returns a snapshot of all registered plugin manifests.
func (m *Manager) AllManifests() map[string]Manifest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make(map[string]Manifest, len(m.manifests))
	for k, v := range m.manifests {
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

	// Cancel owned declarative jobs and periodic scheduler tasks
	m.mu.RLock()
	jbsMgr := m.jobsManager
	schedCl := m.schedCleaner
	m.mu.RUnlock()

	if jbsMgr != nil {
		jbsMgr.CancelByOwner(key)
		jbsMgr.CancelByOwner("plugin:" + key)
	}
	if schedCl != nil {
		schedCl.UnregisterPeriodicTasksByOwner(key)
		schedCl.UnregisterPeriodicTasksByOwner("plugin:" + key)
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

	m.mu.RLock()
	auditor := m.auditor
	m.mu.RUnlock()
	if auditor != nil {
		_ = auditor.Record(ctx, audit.AuditEvent{
			Action: "plugin.disable",
			Target: key,
		})
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

	pctx := m.buildPluginContext(scope.Context(), key, scope)

	var initErr error
	if pci, ok := p.(PluginContextInitializer); ok {
		initErr = pci.InitPlugin(pctx)
	} else if si, ok := p.(ScopeInitializer); ok {
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
	auditor := m.auditor
	m.mu.Unlock()

	if auditor != nil {
		_ = auditor.Record(ctx, audit.AuditEvent{
			Action: "plugin.enable",
			Target: key,
		})
	}

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

// Ensure Manager implements runtime.Component.
var _ runtime.Component = (*Manager)(nil)

// Name returns component identifier for runtime.Component.
func (m *Manager) Name() string {
	return "plugins"
}

// Dependencies returns prerequisite components for runtime.Component.
func (m *Manager) Dependencies() []string {
	return []string{"dispatcher", "jobs"}
}

// Start validates plugin manager state.
func (m *Manager) Start(ctx context.Context) error {
	return nil
}

// Stop gracefully shuts down all managed plugins.
func (m *Manager) Stop(ctx context.Context) error {
	return m.ShutdownWithContext(ctx)
}

// Health evaluates plugin manager health.
func (m *Manager) Health(ctx context.Context) runtime.ComponentHealth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.shutdown {
		return runtime.ComponentHealth{
			Status:  runtime.HealthUnhealthy,
			Details: "plugin manager is shut down",
		}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}
