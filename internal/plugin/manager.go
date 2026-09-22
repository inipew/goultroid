package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

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
	"github.com/inipew/goultroid/internal/services/callback"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
)

// ManifestPlugin is an optional interface plugins can implement to declare their manifest directly.
type ManifestPlugin interface {
	Manifest() Manifest
}

// MessageHookHandler represents the privileged raw Telegram compatibility signature.
type MessageHookHandler = func(ctx context.Context, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) error

// CanonicalMessageHookHandler is the preferred normalized message hook signature.
type CanonicalMessageHookHandler = func(ctx context.Context, message *core.MessageEnvelope) error

// HookRegistrar allows the plugin manager to attach message hooks into the dispatcher.
type HookRegistrar interface {
	AddPrioritizedMessageHandler(priority int, h MessageHookHandler) func()
}

type scopedHookRegistrar interface {
	AddScopedMessageHandler(priority int, scope tasks.ScopeIdentity, h MessageHookHandler) func()
}

type routedHookRegistrar interface {
	AddPrioritizedMessageHandlerWithRouting(priority int, routing core.MessageHookRouting, h MessageHookHandler) func()
}

type scopedRoutedHookRegistrar interface {
	AddScopedMessageHandlerWithRouting(priority int, scope tasks.ScopeIdentity, routing core.MessageHookRouting, h MessageHookHandler) func()
}

type stateRoutedHookRegistrar interface {
	AddPrioritizedMessageHandlerWithRoutingAndState(priority int, routing core.MessageHookRouting, stateGate func(int64) bool, h MessageHookHandler) func()
}

type scopedStateRoutedHookRegistrar interface {
	AddScopedMessageHandlerWithRoutingAndState(priority int, scope tasks.ScopeIdentity, routing core.MessageHookRouting, stateGate func(int64) bool, h MessageHookHandler) func()
}

type canonicalRoutedHookRegistrar interface {
	AddPrioritizedCanonicalMessageHandlerWithRouting(priority int, routing core.MessageHookRouting, h CanonicalMessageHookHandler) func()
}

type scopedCanonicalRoutedHookRegistrar interface {
	AddScopedCanonicalMessageHandlerWithRouting(priority int, scope tasks.ScopeIdentity, routing core.MessageHookRouting, h CanonicalMessageHookHandler) func()
}

type canonicalStateRoutedHookRegistrar interface {
	AddPrioritizedCanonicalMessageHandlerWithRoutingAndState(priority int, routing core.MessageHookRouting, stateGate func(int64) bool, h CanonicalMessageHookHandler) func()
}

type scopedCanonicalStateRoutedHookRegistrar interface {
	AddScopedCanonicalMessageHandlerWithRoutingAndState(priority int, scope tasks.ScopeIdentity, routing core.MessageHookRouting, stateGate func(int64) bool, h CanonicalMessageHookHandler) func()
}

func validateMessageHookAccess(pluginID string, p Plugin, gate *CapabilityGate) error {
	if gate == nil {
		// Compatibility for standalone/unit-test managers. The application
		// composition root always installs a fail-closed capability gate.
		return nil
	}
	if _, canonical := p.(MessageEventPlugin); canonical {
		if err := gate.Check(pluginID, CapTelegramRead); err != nil {
			return fmt.Errorf("canonical message hook requires %s: %w", CapTelegramRead, err)
		}
		return nil
	}
	if _, raw := p.(MessageHookPlugin); raw {
		if err := gate.Check(pluginID, CapTelegramRaw); err != nil {
			return fmt.Errorf("raw message hook requires %s: %w", CapTelegramRaw, err)
		}
	}
	return nil
}

func registerMessageHook(registrar HookRegistrar, p Plugin, scope tasks.ScopeIdentity) (func(), error) {
	if registrar == nil {
		return nil, nil
	}

	if mhp, ok := p.(MessageEventPlugin); ok {
		if stateful, ok := p.(MessageEventStatePlugin); ok {
			routing := stateful.MessageHookRouting()
			if scoped, ok := registrar.(scopedCanonicalStateRoutedHookRegistrar); ok {
				return scoped.AddScopedCanonicalMessageHandlerWithRoutingAndState(mhp.MessageHookPriority(), scope, routing, stateful.MessageHookInterested, mhp.HandleMessageEvent), nil
			}
			if indexed, ok := registrar.(canonicalStateRoutedHookRegistrar); ok {
				return indexed.AddPrioritizedCanonicalMessageHandlerWithRoutingAndState(mhp.MessageHookPriority(), routing, stateful.MessageHookInterested, mhp.HandleMessageEvent), nil
			}
			return nil, fmt.Errorf("hook registrar does not support canonical state routing for plugin %s", p.Name())
		}
		if routed, ok := p.(MessageEventRoutingPlugin); ok {
			routing := routed.MessageHookRouting()
			if scoped, ok := registrar.(scopedCanonicalRoutedHookRegistrar); ok {
				return scoped.AddScopedCanonicalMessageHandlerWithRouting(mhp.MessageHookPriority(), scope, routing, mhp.HandleMessageEvent), nil
			}
			if indexed, ok := registrar.(canonicalRoutedHookRegistrar); ok {
				return indexed.AddPrioritizedCanonicalMessageHandlerWithRouting(mhp.MessageHookPriority(), routing, mhp.HandleMessageEvent), nil
			}
			return nil, fmt.Errorf("hook registrar does not support canonical routing for plugin %s", p.Name())
		}
		return nil, fmt.Errorf("canonical message hook plugin %s must declare MessageHookRouting", p.Name())
	}

	mhp, ok := p.(MessageHookPlugin)
	if !ok {
		return nil, nil
	}
	if stateful, ok := p.(MessageHookStatePlugin); ok {
		routing := stateful.MessageHookRouting()
		if scoped, ok := registrar.(scopedStateRoutedHookRegistrar); ok {
			return scoped.AddScopedMessageHandlerWithRoutingAndState(mhp.MessageHookPriority(), scope, routing, stateful.MessageHookInterested, mhp.HandleIncomingMessage), nil
		}
		if indexed, ok := registrar.(stateRoutedHookRegistrar); ok {
			return indexed.AddPrioritizedMessageHandlerWithRoutingAndState(mhp.MessageHookPriority(), routing, stateful.MessageHookInterested, mhp.HandleIncomingMessage), nil
		}
	}
	if routed, ok := p.(MessageHookRoutingPlugin); ok {
		routing := routed.MessageHookRouting()
		if scoped, ok := registrar.(scopedRoutedHookRegistrar); ok {
			return scoped.AddScopedMessageHandlerWithRouting(mhp.MessageHookPriority(), scope, routing, mhp.HandleIncomingMessage), nil
		}
		if indexed, ok := registrar.(routedHookRegistrar); ok {
			return indexed.AddPrioritizedMessageHandlerWithRouting(mhp.MessageHookPriority(), routing, mhp.HandleIncomingMessage), nil
		}
	}
	if scoped, ok := registrar.(scopedHookRegistrar); ok {
		return scoped.AddScopedMessageHandler(mhp.MessageHookPriority(), scope, mhp.HandleIncomingMessage), nil
	}
	return registrar.AddPrioritizedMessageHandler(mhp.MessageHookPriority(), mhp.HandleIncomingMessage), nil
}

type callbackRegistrar interface {
	RegisterOwned(string, callback.Handler) (callback.Registration, error)
}

// SchedulerTaskCleaner allows the plugin manager to unregister periodic tasks owned by disabled plugins.
type SchedulerTaskCleaner interface {
	UnregisterPeriodicTasksByOwner(owner string) int
}

type Manager struct {
	router            *core.Router
	hookRegistrar     HookRegistrar
	callbackRegistrar callbackRegistrar
	inlineRegistry    *inlineservice.Registry
	schedCleaner      SchedulerTaskCleaner
	resourceManager   *resource.Manager
	gate              *CapabilityGate
	networkService    *network.Service
	processManager    *process.Manager
	filesystemManager *filesystem.Manager
	secretManager     *secret.Manager
	taskClient        tasks.Client
	jobsManager       *jobs.Manager
	storageManager    *storage.Manager
	featureRegistry   *featureRegistry
	savedResponses    *savedresponse.Registry
	plugins           map[string]Plugin
	metadata          map[string]Metadata
	manifests         map[string]Manifest
	scopes            map[string]*Scope
	commands          map[string][]core.Command
	disabled          map[string]bool
	transitions       map[string]string
	teardownErrors    map[string]error
	registering       map[string]bool
	list              []Plugin
	hookCleanups      map[string]func()
	callbackCleanups  map[string]func()
	featureCleanups   map[string]func()
	auditor           audit.Auditor
	panicReporter     core.PanicReporter
	cleanupExecutor   *runtime.CallbackExecutor
	mu                sync.RWMutex
	shutdown          bool
}

func NewManager(router *core.Router) *Manager {
	return &Manager{
		router:           router,
		plugins:          make(map[string]Plugin),
		metadata:         make(map[string]Metadata),
		manifests:        make(map[string]Manifest),
		scopes:           make(map[string]*Scope),
		commands:         make(map[string][]core.Command),
		disabled:         make(map[string]bool),
		transitions:      make(map[string]string),
		teardownErrors:   make(map[string]error),
		registering:      make(map[string]bool),
		hookCleanups:     make(map[string]func()),
		callbackCleanups: make(map[string]func()),
		featureCleanups:  make(map[string]func()),
		featureRegistry:  newFeatureRegistry(),
		savedResponses:   savedresponse.NewRegistry(),
		list:             make([]Plugin, 0),
		cleanupExecutor:  runtime.NewCallbackExecutor(runtime.DefaultLifecycleCallbackConcurrency),
	}
}

func (m *Manager) runLifecycleCallback(ctx context.Context, component string, fn func() error) error {
	if fn == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s skipped after lifecycle deadline: %w", component, err)
	}

	m.mu.RLock()
	executor := m.cleanupExecutor
	reporter := m.panicReporter
	m.mu.RUnlock()
	if executor == nil {
		executor = runtime.NewCallbackExecutor(runtime.DefaultLifecycleCallbackConcurrency)
	}
	err := executor.Run(ctx, fn)
	var panicErr *runtime.CallbackPanicError
	if errors.As(err, &panicErr) {
		if reporter != nil {
			reporter.ReportPanic(core.PanicReport{
				Owner:     "plugin-manager",
				Component: component,
				Value:     panicErr.Value,
				Stack:     panicErr.Stack,
				At:        time.Now().UTC(),
			})
		}
		return fmt.Errorf("%s panic: %w", component, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s exceeded lifecycle deadline: %w", component, err)
	}
	return err
}

// SetCleanupExecutor shares one bounded lifecycle-callback budget across plugin
// shutdown and scope cleanup.
func (m *Manager) SetCleanupExecutor(executor *runtime.CallbackExecutor) {
	if executor == nil {
		return
	}
	m.mu.Lock()
	m.cleanupExecutor = executor
	for _, scope := range m.scopes {
		if scope != nil {
			scope.SetCleanupExecutor(executor)
		}
	}
	m.mu.Unlock()
}

// CleanupStats returns the shared callback executor diagnostics.
func (m *Manager) CleanupStats() runtime.CallbackExecutorStats {
	m.mu.RLock()
	executor := m.cleanupExecutor
	m.mu.RUnlock()
	if executor == nil {
		return runtime.CallbackExecutorStats{}
	}
	return executor.Stats()
}

// SetCallbackRegistrar binds callback registrations to plugin transactions.
func (m *Manager) SetCallbackRegistrar(registrar callbackRegistrar) {
	m.mu.Lock()
	m.callbackRegistrar = registrar
	m.mu.Unlock()
}

// SetInlineRegistry binds feature-owned inline handlers to plugin lifecycle
// transactions. Registration and cleanup remain atomic with the feature catalog.
func (m *Manager) SetInlineRegistry(registry *inlineservice.Registry) {
	m.mu.Lock()
	m.inlineRegistry = registry
	m.mu.Unlock()
}

// SetPlatformServices attaches platform managers and capability gate to this manager.
func (m *Manager) SetPlatformServices(
	gate *CapabilityGate,
	net *network.Service,
	proc *process.Manager,
	fs *filesystem.Manager,
	sec *secret.Manager,
	jbs *jobs.Manager,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gate = gate
	m.networkService = net
	m.processManager = proc
	m.filesystemManager = fs
	m.secretManager = sec
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

// SetPanicReporter attaches a panic reporter for plugin scopes.
func (m *Manager) SetPanicReporter(reporter core.PanicReporter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.panicReporter = reporter
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

// SetTaskClient attaches an execution task client to this plugin manager.
func (m *Manager) SetTaskClient(client tasks.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskClient = client
}

func (m *Manager) buildPluginContext(baseCtx context.Context, name string, scope *Scope) PluginContext {
	m.mu.RLock()
	gate := m.gate
	netSvc := m.networkService
	procMgr := m.processManager
	fsMgr := m.filesystemManager
	secMgr := m.secretManager
	taskClient := m.taskClient
	jobsMgr := m.jobsManager
	storageMgr := m.storageManager
	m.mu.RUnlock()

	return NewPluginContext(baseCtx, ContextConfig{
		Scope:      scope,
		Owner:      name,
		Gate:       gate,
		Network:    netSvc,
		Process:    procMgr,
		Files:      fsMgr,
		Secrets:    secMgr,
		Jobs:       jobsMgr,
		TaskClient: taskClient,
		Storage:    storageMgr,
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

	return m.registerWithContext(ctx, p, &manifest)
}

func (m *Manager) Register(p Plugin) error {
	return m.RegisterWithContext(context.Background(), p)
}

// RegisterWithContext registers and initializes a plugin using the provided startup context.
// It is transactional: no partial registration is visible on failure, and manager mutex is never held while invoking plugin code.
func (m *Manager) RegisterWithContext(ctx context.Context, p Plugin) error {
	return m.registerWithContext(ctx, p, nil)
}

func (m *Manager) registerWithContext(ctx context.Context, p Plugin, suppliedManifest *Manifest) error {
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
	if m.registering[name] {
		m.mu.Unlock()
		return fmt.Errorf("plugin %s registration already in progress", name)
	}
	m.registering[name] = true
	router := m.router
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.registering, name)
		m.mu.Unlock()
	}()

	rollbackManifest := func() {}
	manifestStaged := false
	// Stage manifest capabilities for InitPlugin and compensate on every failure.
	var manifest *Manifest
	if suppliedManifest != nil {
		copy := *suppliedManifest
		manifest = &copy
	} else if mp, ok := p.(ManifestPlugin); ok {
		copy := mp.Manifest()
		manifest = &copy
	}
	if manifest != nil {
		if manifest.ID == "" {
			manifest.ID = name
		}
		if manifest.Name == "" {
			manifest.Name = p.Name()
		}
		if err := manifest.Validate(); err != nil {
			return fmt.Errorf("invalid manifest for plugin %s: %w", name, err)
		}
		var err error
		rollbackManifest, err = m.stageManifest(name, *manifest)
		if err != nil {
			return fmt.Errorf("gate rejected manifest for plugin %s: %w", name, err)
		}
		manifestStaged = true
	}
	committed := false
	defer func() {
		if manifestStaged && !committed {
			rollbackManifest()
		}
	}()

	m.mu.RLock()
	messageGate := m.gate
	m.mu.RUnlock()
	if err := validateMessageHookAccess(name, p, messageGate); err != nil {
		return fmt.Errorf("plugin %s message hook rejected: %w", name, err)
	}

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
	reporter := m.panicReporter
	cleanupExecutor := m.cleanupExecutor
	m.mu.RUnlock()

	var scope *Scope
	if resMgr != nil {
		scope = NewScopeWithManager(ctx, "plugin:"+name, resMgr)
	} else {
		scope = NewScope(ctx, "plugin:"+name)
	}
	if reporter != nil {
		scope.SetPanicReporter(reporter)
	}
	if cleanupExecutor != nil {
		scope.SetCleanupExecutor(cleanupExecutor)
	}
	commandScope := tasks.ScopeIdentity{Owner: "plugin:" + name, Generation: scope.Generation()}
	for i := range cmds {
		cmds[i].Scope = commandScope
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
		if s, ok := p.(ContextShutdowner); ok {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" init rollback", func() error { return s.ShutdownContext(ctx) })
		} else if s, ok := p.(Shutdowner); ok {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" init rollback", s.Shutdown)
		}
		_ = scope.Close(ctx)
		return fmt.Errorf("failed to initialize plugin %s: %w", name, initErr)
	}

	cleanupPlugin := func() {
		if s, ok := p.(ContextShutdowner); ok {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" registration rollback", func() error { return s.ShutdownContext(ctx) })
		} else if s, ok := p.(Shutdowner); ok {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" registration rollback", s.Shutdown)
		}
		_ = scope.Close(ctx)
	}

	// 4. Prepare router, hooks, and metadata outside the manager lock. The
	// registration reservation prevents a competing commit for the same name.
	if err := router.RegisterBatch(cmds); err != nil {
		cleanupPlugin()
		return fmt.Errorf("plugin %s command registration failed: %w", name, err)
	}

	var hookCleanup func()
	m.mu.RLock()
	hookRegistrar := m.hookRegistrar
	m.mu.RUnlock()
	if hookRegistrar != nil {
		var hookErr error
		hookCleanup, hookErr = registerMessageHook(hookRegistrar, p, commandScope)
		if hookErr != nil {
			router.UnregisterBatch(cmds)
			cleanupPlugin()
			return fmt.Errorf("plugin %s message hook registration failed: %w", name, hookErr)
		}
	}
	var callbackCleanup func()
	m.mu.RLock()
	callbackRegistry := m.callbackRegistrar
	m.mu.RUnlock()
	if callbackRegistry != nil {
		if handler, ok := p.(callback.Handler); ok {
			registration, err := callbackRegistry.RegisterOwned(name, handler)
			if err != nil {
				if hookCleanup != nil {
					_ = m.runLifecycleCallback(ctx, "plugin "+name+" hook registration rollback", func() error {
						hookCleanup()
						return nil
					})
				}
				router.UnregisterBatch(cmds)
				cleanupPlugin()
				return fmt.Errorf("plugin %s callback registration failed: %w", name, err)
			}
			callbackCleanup = registration.Close
		}
	}

	featureCleanup, err := m.registerFeatureContract(name, p, commandScope, cmds)
	if err != nil {
		if callbackCleanup != nil {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" callback feature rollback", func() error { callbackCleanup(); return nil })
		}
		if hookCleanup != nil {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" hook feature rollback", func() error { hookCleanup(); return nil })
		}
		router.UnregisterBatch(cmds)
		cleanupPlugin()
		return fmt.Errorf("plugin %s feature contract registration failed: %w", name, err)
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

	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		if featureCleanup != nil {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" feature shutdown rollback", func() error { featureCleanup(); return nil })
		}
		if hookCleanup != nil {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" hook shutdown rollback", func() error { hookCleanup(); return nil })
		}
		if callbackCleanup != nil {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" callback shutdown rollback", func() error { callbackCleanup(); return nil })
		}
		router.UnregisterBatch(cmds)
		cleanupPlugin()
		return fmt.Errorf("plugin manager is shutting down")
	}
	if _, exists := m.plugins[name]; exists {
		m.mu.Unlock()
		if featureCleanup != nil {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" feature duplicate rollback", func() error { featureCleanup(); return nil })
		}
		if hookCleanup != nil {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" hook duplicate rollback", func() error { hookCleanup(); return nil })
		}
		if callbackCleanup != nil {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" callback duplicate rollback", func() error { callbackCleanup(); return nil })
		}
		router.UnregisterBatch(cmds)
		cleanupPlugin()
		return fmt.Errorf("plugin already registered: %s", name)
	}
	m.plugins[name] = p
	if manifest != nil {
		m.manifests[name] = *manifest
	}
	m.metadata[name] = meta
	m.scopes[name] = scope
	m.commands[name] = append([]core.Command(nil), cmds...)
	m.list = append(m.list, p)
	if hookCleanup != nil {
		m.hookCleanups[name] = hookCleanup
	}
	if callbackCleanup != nil {
		m.callbackCleanups[name] = callbackCleanup
	}
	if featureCleanup != nil {
		m.featureCleanups[name] = featureCleanup
	}
	m.mu.Unlock()
	committed = true
	return nil
}

func (m *Manager) stageManifest(name string, manifest Manifest) (func(), error) {
	m.mu.RLock()
	gate := m.gate
	m.mu.RUnlock()
	if gate != nil {
		rollback, err := gate.StageManifest(manifest)
		if err != nil {
			return nil, err
		}
		return rollback, nil
	}
	return func() {}, nil
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

// ShutdownWithContext shuts plugins down in reverse registration order. Every
// plugin/hook callback is bounded by ctx. Context-aware plugins receive ctx
// directly; legacy callbacks run behind the same deadline boundary so they
// cannot hold the application past its global shutdown budget.
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
	cleanups := make([]func(), 0, len(m.featureCleanups)+len(m.hookCleanups)+len(m.callbackCleanups))
	for _, cleanup := range m.featureCleanups {
		cleanups = append(cleanups, cleanup)
	}
	m.featureCleanups = make(map[string]func())
	for _, cleanup := range m.hookCleanups {
		cleanups = append(cleanups, cleanup)
	}
	m.hookCleanups = make(map[string]func())
	for _, cleanup := range m.callbackCleanups {
		cleanups = append(cleanups, cleanup)
	}
	m.callbackCleanups = make(map[string]func())
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

	var errs []error

	// Quiesce TaskEngine ownership before detaching registrations. This prevents
	// already-admitted work from crossing a plugin generation boundary while
	// callbacks/hooks/features are being removed.
	m.mu.RLock()
	shutdownTaskClient := m.taskClient
	m.mu.RUnlock()
	if shutdownTaskClient != nil {
		for _, scope := range scopes {
			if scope != nil {
				shutdownTaskClient.CancelScope(tasks.ScopeIdentity{Owner: scope.Owner(), Generation: scope.Generation()}, tasks.CauseShutdown)
			}
		}
	}

	// 1. Detach all feature surfaces, message hooks, and callbacks before plugin shutdown.
	for i, cleanup := range cleanups {
		if cleanup == nil {
			continue
		}
		if err := m.runLifecycleCallback(ctx, fmt.Sprintf("plugin registration cleanup %d", i), func() error {
			cleanup()
			return nil
		}); err != nil {
			errs = append(errs, err)
		}
	}

	m.mu.RLock()
	featureRegistry := m.featureRegistry
	m.mu.RUnlock()
	if featureRegistry != nil && featureRegistry.interactions != nil {
		if err := featureRegistry.interactions.Close(); err != nil {
			errs = append(errs, fmt.Errorf("interaction runtime: %w", err))
		}
	}

	// 2. Shut down plugins in reverse registration order.
	for i := len(plugins) - 1; i >= 0; i-- {
		p := plugins[i]
		var err error
		if s, ok := p.(ContextShutdowner); ok {
			err = m.runLifecycleCallback(ctx, "plugin "+p.Name()+" shutdown", func() error {
				return s.ShutdownContext(ctx)
			})
		} else if s, ok := p.(Shutdowner); ok {
			err = m.runLifecycleCallback(ctx, "plugin "+p.Name()+" legacy shutdown", s.Shutdown)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
		}
		if scope := scopes[strings.ToLower(strings.TrimSpace(p.Name()))]; scope != nil {
			if err := scope.Close(ctx); err != nil {
				errs = append(errs, fmt.Errorf("%s scope: %w", p.Name(), err))
			}
		}
		m.router.UnregisterBatch(commands[strings.ToLower(strings.TrimSpace(p.Name()))])
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors during plugin shutdown: %w", errors.Join(errs...))
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
	if transition := m.transitions[key]; transition != "" {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q is currently %s", name, transition)
	}
	cmds := append([]core.Command(nil), m.commands[key]...)
	scope := m.scopes[key]
	hookCleanup := m.hookCleanups[key]
	delete(m.hookCleanups, key)
	callbackCleanup := m.callbackCleanups[key]
	delete(m.callbackCleanups, key)
	featureCleanup := m.featureCleanups[key]
	delete(m.featureCleanups, key)
	delete(m.scopes, key)
	m.disabled[key] = true
	m.transitions[key] = "disabling"
	router := m.router
	taskClient := m.taskClient
	m.mu.Unlock()

	// Quiesce this generation before registrations are detached. New callback
	// admission already fails because m.scopes no longer exposes this scope.
	if scope != nil && taskClient != nil {
		taskClient.CancelScope(tasks.ScopeIdentity{Owner: scope.Owner(), Generation: scope.Generation()}, tasks.CauseScopeClosed)
	}

	var errs []error
	if featureCleanup != nil {
		if err := m.runLifecycleCallback(ctx, "plugin "+name+" feature cleanup", func() error {
			featureCleanup()
			return nil
		}); err != nil {
			errs = append(errs, err)
		}
	}
	if hookCleanup != nil {
		if err := m.runLifecycleCallback(ctx, "plugin "+name+" hook cleanup", func() error {
			hookCleanup()
			return nil
		}); err != nil {
			errs = append(errs, err)
		}
	}
	if callbackCleanup != nil {
		if err := m.runLifecycleCallback(ctx, "plugin "+name+" callback cleanup", func() error {
			callbackCleanup()
			return nil
		}); err != nil {
			errs = append(errs, err)
		}
	}

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

	// Close scope and execute shutdown hooks.
	if s, ok := p.(ContextShutdowner); ok {
		if err := m.runLifecycleCallback(ctx, "plugin "+name+" shutdown", func() error {
			return s.ShutdownContext(ctx)
		}); err != nil {
			errs = append(errs, fmt.Errorf("shutdown %s: %w", name, err))
		}
	} else if s, ok := p.(Shutdowner); ok {
		if err := m.runLifecycleCallback(ctx, "plugin "+name+" legacy shutdown", s.Shutdown); err != nil {
			errs = append(errs, fmt.Errorf("shutdown %s: %w", name, err))
		}
	}

	if scope != nil {
		if err := scope.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close scope %s: %w", name, err))
		}
	}

	if len(errs) > 0 {
		disableErr := fmt.Errorf("errors disabling plugin %s: %w", name, errors.Join(errs...))
		m.mu.Lock()
		delete(m.transitions, key)
		m.teardownErrors[key] = disableErr
		m.mu.Unlock()
		return disableErr
	}
	m.mu.Lock()
	delete(m.transitions, key)
	delete(m.teardownErrors, key)
	m.mu.Unlock()

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
	if transition := m.transitions[key]; transition != "" {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q is currently %s", name, transition)
	}
	if teardownErr := m.teardownErrors[key]; teardownErr != nil {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q has incomplete teardown: %w", name, teardownErr)
	}
	m.transitions[key] = "enabling"
	cmds := append([]core.Command(nil), m.commands[key]...)
	router := m.router
	resMgr := m.resourceManager
	reporter := m.panicReporter
	cleanupExecutor := m.cleanupExecutor
	messageGate := m.gate
	m.mu.Unlock()

	if err := validateMessageHookAccess(key, p, messageGate); err != nil {
		m.mu.Lock()
		delete(m.transitions, key)
		m.mu.Unlock()
		return fmt.Errorf("plugin %s message hook rejected: %w", name, err)
	}

	// Initialize scope and plugin
	var scope *Scope
	if resMgr != nil {
		scope = NewScopeWithManager(ctx, "plugin:"+key, resMgr)
	} else {
		scope = NewScope(ctx, "plugin:"+key)
	}
	if reporter != nil {
		scope.SetPanicReporter(reporter)
	}
	if cleanupExecutor != nil {
		scope.SetCleanupExecutor(cleanupExecutor)
	}
	commandScope := tasks.ScopeIdentity{Owner: "plugin:" + key, Generation: scope.Generation()}
	for i := range cmds {
		cmds[i].Scope = commandScope
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
		if s, ok := p.(ContextShutdowner); ok {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" enable rollback", func() error { return s.ShutdownContext(ctx) })
		} else if s, ok := p.(Shutdowner); ok {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" enable rollback", s.Shutdown)
		}
		_ = scope.Close(ctx)
		m.mu.Lock()
		delete(m.transitions, key)
		m.mu.Unlock()
		return fmt.Errorf("failed to re-initialize plugin %s: %w", name, initErr)
	}

	cleanupEnabledPlugin := func(component string) {
		if s, ok := p.(ContextShutdowner); ok {
			_ = m.runLifecycleCallback(ctx, component, func() error { return s.ShutdownContext(ctx) })
		} else if s, ok := p.(Shutdowner); ok {
			_ = m.runLifecycleCallback(ctx, component, s.Shutdown)
		}
		_ = scope.Close(ctx)
	}

	var hookCleanup func()
	m.mu.RLock()
	hookRegistrar := m.hookRegistrar
	m.mu.RUnlock()
	if hookRegistrar != nil {
		var hookErr error
		hookCleanup, hookErr = registerMessageHook(hookRegistrar, p, commandScope)
		if hookErr != nil {
			cleanupEnabledPlugin("plugin " + name + " hook registration rollback")
			m.mu.Lock()
			delete(m.transitions, key)
			m.mu.Unlock()
			return fmt.Errorf("failed to re-register message hook for plugin %s: %w", name, hookErr)
		}
	}
	var callbackCleanup func()
	m.mu.RLock()
	callbackRegistry := m.callbackRegistrar
	m.mu.RUnlock()
	if callbackRegistry != nil {
		if handler, ok := p.(callback.Handler); ok {
			registration, regErr := callbackRegistry.RegisterOwned(key, handler)
			if regErr != nil {
				if hookCleanup != nil {
					_ = m.runLifecycleCallback(ctx, "plugin "+name+" hook enable rollback", func() error { hookCleanup(); return nil })
				}
				_ = scope.Close(ctx)
				m.mu.Lock()
				delete(m.transitions, key)
				m.mu.Unlock()
				return fmt.Errorf("failed to re-register callback for plugin %s: %w", name, regErr)
			}
			callbackCleanup = registration.Close
		}
	}

	// Register commands back to router.
	if router != nil && len(cmds) > 0 {
		if err := router.RegisterBatch(cmds); err != nil {
			if hookCleanup != nil {
				_ = m.runLifecycleCallback(ctx, "plugin "+name+" hook command rollback", func() error { hookCleanup(); return nil })
			}
			if callbackCleanup != nil {
				_ = m.runLifecycleCallback(ctx, "plugin "+name+" callback command rollback", func() error { callbackCleanup(); return nil })
			}
			_ = scope.Close(ctx)
			m.mu.Lock()
			delete(m.transitions, key)
			m.mu.Unlock()
			return fmt.Errorf("failed to re-register commands for plugin %s: %w", name, err)
		}
	}

	featureCleanup, err := m.registerFeatureContract(key, p, commandScope, cmds)
	if err != nil {
		if router != nil && len(cmds) > 0 {
			router.UnregisterBatch(cmds)
		}
		if callbackCleanup != nil {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" callback feature rollback", func() error { callbackCleanup(); return nil })
		}
		if hookCleanup != nil {
			_ = m.runLifecycleCallback(ctx, "plugin "+name+" hook feature rollback", func() error { hookCleanup(); return nil })
		}
		_ = scope.Close(ctx)
		m.mu.Lock()
		delete(m.transitions, key)
		m.mu.Unlock()
		return fmt.Errorf("failed to re-register feature contract for plugin %s: %w", name, err)
	}

	m.mu.Lock()
	m.scopes[key] = scope
	m.commands[key] = append([]core.Command(nil), cmds...)
	if hookCleanup != nil {
		m.hookCleanups[key] = hookCleanup
	}
	if callbackCleanup != nil {
		m.callbackCleanups[key] = callbackCleanup
	}
	if featureCleanup != nil {
		m.featureCleanups[key] = featureCleanup
	}
	delete(m.disabled, key)
	delete(m.transitions, key)
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
	if len(m.teardownErrors) > 0 {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "one or more plugins have incomplete teardown"}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}
