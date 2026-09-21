package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/inipew/goultroid/internal/addon"
	"github.com/inipew/goultroid/internal/assistant"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/idempotency"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/scheduler"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/inline"
	mediaSvc "github.com/inipew/goultroid/internal/services/media"
	processSvc "github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/services/ratelimit"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/telegram"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type App struct {
	cfg              *config.Config
	logger           *zap.Logger
	db               *database.DB
	client           *telegram.Client
	plugins          *plugin.Manager
	router           *core.Router
	sched            *scheduler.Engine
	eventBus         *core.EventBus
	assistant        assistant.Client
	limiter          *ratelimit.Limiter
	interLimiter     *ratelimit.Limiter
	addonMgr         *addon.Manager
	callbackStore    *callback.StateStore
	inlineEngine     *inline.Engine
	settingsService  *settings.Service
	settingsLive     *settings.LiveBinder
	media            *mediaSvc.Service
	downloadRegistry *download.Registry
	processRunner    *processSvc.OSRunner
	startTime        time.Time

	jobs            *jobs.Manager
	taskEngine      *taskengine.Engine
	persistencePump *jobs.PersistencePump
	resources       *resource.Manager
	idemp           *idempotency.Manager
	runtime         *runtime.Runtime
	supervisor      *runtime.Supervisor

	appCancel       context.CancelFunc
	transportCancel context.CancelFunc
	transportDone   <-chan struct{}

	lifecycleMu    sync.Mutex
	lifecycleState atomic.Uint32
	shutdownDone   chan struct{}
	shutdownErr    error
}

func New(cfg *config.Config) (_ *App, retErr error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	logger, logLevel, err := initLogger(cfg.LogLevel)
	if err != nil {
		return nil, fmt.Errorf("failed to build logger: %w", err)
	}
	var coreDeps *coreDependencies
	defer func() {
		if retErr == nil {
			return
		}
		cleanupCore(coreDeps, logger)
		if err := logger.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
			retErr = errors.Join(retErr, fmt.Errorf("sync logger after construction failure: %w", err))
		}
	}()

	coreDeps, err = buildCore(cfg, logger)
	if err != nil {
		return nil, err
	}
	tgRuntime, err := buildTelegramRuntime(cfg, coreDeps, logger)
	if err != nil {
		return nil, err
	}
	domServices, err := buildDomainServices(cfg, coreDeps, tgRuntime, logger)
	if err != nil {
		return nil, err
	}

	delayedActions := newDelayedActionScheduler(coreDeps.taskEngine)
	tgRuntime.dispatcher.Executor().SetDelayedActions(delayedActions)

	pluginManager := plugin.NewManager(coreDeps.router)
	pluginManager.SetCleanupExecutor(coreDeps.cleanupExecutor)
	pluginManager.SetPanicReporter(zapCorePanicReporter{logger: logger.Named("plugin.panic")})
	coreDeps.eventBus.SetTasks(coreDeps.taskEngine)
	pluginManager.SetHookRegistrar(tgRuntime.dispatcher)
	pluginManager.SetCallbackRegistrar(coreDeps.callbackRouter)
	tgRuntime.dispatcher.SetPluginScopeResolver(func(owner string) (tasks.ScopeIdentity, bool) {
		scope, ok := pluginManager.Scope(owner)
		if !ok {
			return tasks.ScopeIdentity{}, false
		}
		return tasks.ScopeIdentity{Owner: scope.Owner(), Generation: scope.Generation()}, true
	})
	if coreDeps.resourceManager != nil {
		pluginManager.SetResourceManager(coreDeps.resourceManager)
	}
	if coreDeps.auditService != nil {
		pluginManager.SetAuditor(coreDeps.auditService)
	}
	if coreDeps.storageManager != nil {
		pluginManager.SetStorageManager(coreDeps.storageManager)
	}
	pluginManager.SetPlatformServices(
		coreDeps.capGate,
		coreDeps.netService,
		coreDeps.procManager,
		coreDeps.fsManager,
		coreDeps.secretManager,
		coreDeps.jobsManager,
	)
	pluginManager.SetTaskClient(coreDeps.taskEngine)
	if domServices.schedEngine != nil {
		pluginManager.SetSchedulerCleaner(domServices.schedEngine)
	}
	if domServices.addonManager != nil {
		domServices.addonManager.SetProcessManager(coreDeps.procManager)
		domServices.addonManager.SetRuntimeBoundary(coreDeps.eventBus, coreDeps.taskEngine)
		domServices.addonManager.SetCommandRouter(coreDeps.router)
	}
	if tgRuntime.assistant != nil {
		if aware, ok := tgRuntime.assistant.(interface {
			SetDelayedActions(core.DelayedActionScheduler)
		}); ok {
			aware.SetDelayedActions(delayedActions)
		}
		tgRuntime.assistant.SetCoreRouter(coreDeps.router)
		tgRuntime.assistant.SetSettingsService(domServices.settingsService)
		tgRuntime.assistant.SetMetricsCollector(coreDeps.metrics)
		tgRuntime.assistant.SetCallbackRouter(coreDeps.callbackRouter)
		tgRuntime.assistant.SetInlineEngine(coreDeps.inlineEngine)
		tgRuntime.assistant.SetTasks(coreDeps.taskEngine)
		tgRuntime.assistant.SetPluginScopeResolver(func(owner string) (tasks.ScopeIdentity, bool) {
			scope, ok := pluginManager.Scope(owner)
			if !ok {
				return tasks.ScopeIdentity{}, false
			}
			return tasks.ScopeIdentity{Owner: scope.Owner(), Generation: scope.Generation()}, true
		})
	}

	if err := migrateBuiltinFeatures(context.Background(), coreDeps.db); err != nil {
		return nil, err
	}
	if _, err := reconcileBuiltinPersistentMedia(context.Background(), coreDeps.db, domServices.storage); err != nil {
		return nil, err
	}

	featureRuntime := &module.Runtime{
		OwnerID:   coreDeps.perms.OwnerID,
		Logger:    domServices.logger,
		StartTime: domServices.startTime,
		CoreRuntime: module.CoreRuntime{
			DB:          coreDeps.db,
			Permissions: coreDeps.perms,
			Plugins:     pluginManager,
			Router:      coreDeps.router,
			EventBus:    coreDeps.eventBus,
			Metrics:     coreDeps.metrics,
		},
		TelegramRuntime: module.TelegramRuntime{
			TelegramService: tgRuntime.client.Service,
			Resolver:        tgRuntime.dispatcher.Resolver(),
			Callbacks:       coreDeps.callbackRouter,
			CallbackStore:   coreDeps.callbackStore,
		},
		ServiceRuntime: module.ServiceRuntime{
			Storage:          domServices.storage,
			DownloadRegistry: domServices.downloadRegistry,
			MediaService:     domServices.mediaService,
			PMPermitService:  domServices.pmpermitService,
			BroadcastService: domServices.broadcastService,
			UserlogService:   domServices.userlogService,
			AddonManager:     domServices.addonManager,
			SettingsService:  domServices.settingsService,
			SchedEngine:      domServices.schedEngine,
		},
		PlatformRuntime: module.PlatformRuntime{
			Gate:       coreDeps.capGate,
			Network:    coreDeps.netService,
			Process:    coreDeps.procManager,
			Files:      coreDeps.fsManager,
			Secrets:    coreDeps.secretManager,
			Audit:      coreDeps.auditService,
			Resources:  coreDeps.resourceManager,
			Jobs:       coreDeps.jobsManager,
			TaskEngine: coreDeps.taskEngine,
		},
	}
	if err := registerBuiltinModules(context.Background(), featureRuntime); err != nil {
		return nil, err
	}

	settingsLive, err := buildLiveSettingsBinder(coreDeps, domServices, pluginManager, logLevel)
	if err != nil {
		return nil, fmt.Errorf("build live settings binder: %w", err)
	}
	if err := pluginManager.RegisterWithContext(context.Background(), assistantshell.NewFeature()); err != nil {
		return nil, fmt.Errorf("register assistant shell feature: %w", err)
	}
	if tgRuntime.assistant != nil {
		drivers := make([]assistantinteraction.FeatureDriver, 0)
		for _, registered := range pluginManager.Plugins() {
			if driver, ok := registered.(assistantinteraction.FeatureDriver); ok {
				drivers = append(drivers, driver)
			}
		}
		tgRuntime.assistant.SetInteractionDrivers(drivers)
		tgRuntime.assistant.SetInteractionFoundation(pluginManager.FeatureCatalog(), pluginManager.InteractionRuntime(), pluginManager.ActionDispatcher())
	}

	rt := runtime.New()
	resources := []resourceComponent{
		{name: "database", stop: coreDeps.db.Close},
		{name: "idempotency", dependencies: []string{"database"}, start: coreDeps.idempManager.Start, stopContext: coreDeps.idempManager.Stop},
		{name: "command-rate-limiter", dependencies: []string{"database"}, start: coreDeps.cmdLimiter.Start, stop: coreDeps.cmdLimiter.Close},
		{name: "interaction-rate-limiter", dependencies: []string{"database"}, start: coreDeps.interLimiter.Start, stop: coreDeps.interLimiter.Close},
		{name: "addon-runtimes", dependencies: []string{"database", "eventbus", "taskengine"}, stop: domServices.addonManager.ShutdownRuntimes},
	}
	for _, resource := range resources {
		if err := rt.Register(resource); err != nil {
			return nil, fmt.Errorf("register %s component: %w", resource.name, err)
		}
	}
	if err := rt.Register(dependencyComponent{Component: coreDeps.eventBus, dependencies: []string{"database", "taskengine"}}); err != nil {
		return nil, fmt.Errorf("register eventbus component: %w", err)
	}
	if coreDeps.persistencePump != nil {
		if err := rt.Register(coreDeps.persistencePump); err != nil {
			return nil, fmt.Errorf("register persistence-pump component: %w", err)
		}
	}
	if coreDeps.taskEngine != nil {
		if err := rt.Register(dependencyComponent{Component: coreDeps.taskEngine, dependencies: []string{"persistence-pump"}}); err != nil {
			return nil, fmt.Errorf("register taskengine component: %w", err)
		}
	}
	if err := rt.Register(delayedActions); err != nil {
		return nil, fmt.Errorf("register delayed-actions component: %w", err)
	}
	if err := rt.Register(dependencyComponent{Component: coreDeps.jobsManager, dependencies: []string{"taskengine", "eventbus"}}); err != nil {
		return nil, fmt.Errorf("register jobs component: %w", err)
	}
	if err := rt.Register(domServices.schedEngine); err != nil {
		return nil, fmt.Errorf("register scheduler component: %w", err)
	}
	if coreDeps.callbackStore != nil {
		if err := rt.Register(coreDeps.callbackStore); err != nil {
			return nil, fmt.Errorf("register callback_store component: %w", err)
		}
	}
	if coreDeps.inlineEngine != nil && coreDeps.inlineEngine.Cache() != nil {
		if err := rt.Register(coreDeps.inlineEngine.Cache()); err != nil {
			return nil, fmt.Errorf("register inline_cache component: %w", err)
		}
	}
	if domServices.settingsService != nil {
		if err := rt.Register(domServices.settingsService); err != nil {
			return nil, fmt.Errorf("register settings component: %w", err)
		}
	}
	if settingsLive != nil {
		if err := rt.Register(settingsLive); err != nil {
			return nil, fmt.Errorf("register live settings component: %w", err)
		}
	}
	if err := rt.Register(dependencyComponent{Component: tgRuntime.dispatcher, dependencies: []string{"eventbus", "taskengine", "settings-live"}}); err != nil {
		return nil, fmt.Errorf("register dispatcher component: %w", err)
	}
	if tgRuntime.assistant != nil {
		if err := rt.Register(tgRuntime.assistant); err != nil {
			return nil, fmt.Errorf("register assistant component: %w", err)
		}
	}
	supervisor := runtime.NewSupervisor(
		runtime.WithSupervisorName("lifecycle-supervisor"),
		runtime.WithPanicReporter(zapRuntimePanicReporter{logger: logger.Named("supervisor")}),
	)
	if tgRuntime.client != nil {
		if err := supervisor.Register(runtime.WorkerSpec{
			Name:    "telegram-dialogs-warmup",
			Restart: runtime.NeverRestart,
			Run: func(ctx context.Context) error {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-tgRuntime.client.Ready():
					return tgRuntime.client.PreloadDialogs(ctx)
				}
			},
		}); err != nil {
			return nil, fmt.Errorf("register Telegram dialog warmup worker: %w", err)
		}
	}
	if tgRuntime.client != nil {
		if err := supervisor.Register(runtime.WorkerSpec{
			Name:    "telegram-restart-notification",
			Restart: runtime.NeverRestart,
			Run: func(ctx context.Context) error {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-tgRuntime.client.Ready():
					return tgRuntime.client.NotifyRestartState(ctx)
				}
			},
		}); err != nil {
			return nil, fmt.Errorf("register Telegram restart notification worker: %w", err)
		}
	}
	if err := rt.Register(supervisor); err != nil {
		return nil, fmt.Errorf("register supervisor component: %w", err)
	}

	if err := rt.Register(dependencyComponent{Component: pluginManager, dependencies: []string{"dispatcher", "jobs", "addon-runtimes"}}); err != nil {
		return nil, fmt.Errorf("register plugins component: %w", err)
	}

	return &App{
		cfg:              cfg,
		logger:           logger,
		db:               coreDeps.db,
		client:           tgRuntime.client,
		plugins:          pluginManager,
		router:           coreDeps.router,
		sched:            domServices.schedEngine,
		eventBus:         coreDeps.eventBus,
		assistant:        tgRuntime.assistant,
		limiter:          coreDeps.cmdLimiter,
		interLimiter:     coreDeps.interLimiter,
		addonMgr:         domServices.addonManager,
		callbackStore:    coreDeps.callbackStore,
		inlineEngine:     coreDeps.inlineEngine,
		settingsService:  domServices.settingsService,
		settingsLive:     settingsLive,
		media:            domServices.mediaService,
		downloadRegistry: domServices.downloadRegistry,
		processRunner:    domServices.processRunner,
		startTime:        domServices.startTime,
		jobs:             coreDeps.jobsManager,
		taskEngine:       coreDeps.taskEngine,
		persistencePump:  coreDeps.persistencePump,
		resources:        coreDeps.resourceManager,
		idemp:            coreDeps.idempManager,
		runtime:          rt,
		supervisor:       supervisor,
		shutdownDone:     make(chan struct{}),
	}, nil
}

func (a *App) Run(ctx context.Context) error { return a.runLifecycle(ctx) }

func initLogger(logLevel string) (*zap.Logger, zap.AtomicLevel, error) {
	var zapLevel zapcore.Level
	switch logLevel {
	case "debug":
		zapLevel = zap.DebugLevel
	case "warn":
		zapLevel = zap.WarnLevel
	case "error":
		zapLevel = zap.ErrorLevel
	default:
		zapLevel = zap.InfoLevel
	}
	atomicLevel := zap.NewAtomicLevelAt(zapLevel)
	zapCfg := zap.NewDevelopmentConfig()
	zapCfg.Level = atomicLevel
	logger, err := zapCfg.Build()
	if err != nil {
		return nil, atomicLevel, err
	}
	return logger, atomicLevel, nil
}

type lifecycleState uint32

const (
	lifecycleNew lifecycleState = iota
	lifecycleStarting
	lifecycleRunning
	lifecycleQuiescing
	lifecycleStopping
	lifecycleStopped
	lifecycleFailed
)

func (a *App) state() lifecycleState { return lifecycleState(a.lifecycleState.Load()) }

func (a *App) beginStart() error {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if s := lifecycleState(a.lifecycleState.Load()); s != lifecycleNew {
		return fmt.Errorf("application cannot start from state %d", s)
	}
	a.lifecycleState.Store(uint32(lifecycleStarting))
	return nil
}

func (a *App) markRunning() {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if lifecycleState(a.lifecycleState.Load()) == lifecycleStarting {
		a.lifecycleState.Store(uint32(lifecycleRunning))
	}
}

func (a *App) beginQuiesce() bool {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	s := lifecycleState(a.lifecycleState.Load())
	if s == lifecycleStopped || s == lifecycleStopping || s == lifecycleQuiescing {
		return false
	}
	a.lifecycleState.Store(uint32(lifecycleQuiescing))
	return true
}

func (a *App) markStopping() {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	a.lifecycleState.Store(uint32(lifecycleStopping))
}

func (a *App) markStopped(err error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	a.shutdownErr = err
	a.lifecycleState.Store(uint32(lifecycleStopped))
	select {
	case <-a.shutdownDone:
	default:
		close(a.shutdownDone)
	}
}

func (a *App) LifecycleState() string {
	switch a.state() {
	case lifecycleNew:
		return "new"
	case lifecycleStarting:
		return "starting"
	case lifecycleRunning:
		return "running"
	case lifecycleQuiescing:
		return "quiescing"
	case lifecycleStopping:
		return "stopping"
	case lifecycleStopped:
		return "stopped"
	case lifecycleFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Supervisor returns the application lifecycle supervisor.
func (a *App) Supervisor() *runtime.Supervisor {
	return a.supervisor
}

// DBMetrics returns the active database metrics collector.
func (a *App) DBMetrics() database.DBMetrics {
	if a != nil && a.db != nil {
		return a.db.Metrics()
	}
	return database.NoopDBMetrics{}
}

// RPCMetrics returns the active Telegram RPC metrics collector.
func (a *App) RPCMetrics() telegram.RPCMetrics {
	if a != nil && a.client != nil && a.client.Executor() != nil {
		return a.client.Executor().Metrics()
	}
	return telegram.NoopRPCMetrics{}
}

type zapCorePanicReporter struct {
	logger *zap.Logger
}

func (r zapCorePanicReporter) ReportPanic(report core.PanicReport) {
	if r.logger == nil {
		return
	}
	r.logger.Error("recovered panic",
		zap.String("owner", report.Owner),
		zap.String("component", report.Component),
		zap.Any("value", report.Value),
		zap.ByteString("stack", report.Stack),
		zap.Time("at", report.At),
	)
}

type zapRuntimePanicReporter struct {
	logger *zap.Logger
}

func (r zapRuntimePanicReporter) ReportPanic(report runtime.PanicReport) {
	if r.logger == nil {
		return
	}
	r.logger.Error("recovered panic",
		zap.String("owner", report.Owner),
		zap.String("component", report.Component),
		zap.Any("value", report.Value),
		zap.ByteString("stack", report.Stack),
		zap.Time("at", report.At),
	)
}
