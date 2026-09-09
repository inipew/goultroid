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
	"github.com/inipew/goultroid/internal/services/inline"
	mediaSvc "github.com/inipew/goultroid/internal/services/media"
	processSvc "github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/services/ratelimit"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/telegram"
	"github.com/inipew/goultroid/internal/workers"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type App struct {
	cfg             *config.Config
	logger          *zap.Logger
	db              *database.DB
	client          *telegram.Client
	plugins         *plugin.Manager
	router          *core.Router
	sched           *scheduler.Engine
	eventBus        *core.EventBus
	assistant       assistant.Client
	limiter         *ratelimit.Limiter
	interLimiter    *ratelimit.Limiter
	addonMgr        *addon.Manager
	callbackStore   *callback.StateStore
	inlineEngine    *inline.Engine
	settingsService *settings.Service
	media           *mediaSvc.Service
	processRunner   *processSvc.OSRunner
	startTime       time.Time

	workers   *workers.Manager
	tasks     *tasks.Manager
	jobs      *jobs.Manager
	resources *resource.Manager
	idemp     *idempotency.Manager
	runtime   *runtime.Runtime

	lifecycleMu    sync.Mutex
	lifecycleState atomic.Uint32
	shutdownDone   chan struct{}
	shutdownErr    error
}

func New(cfg *config.Config) (_ *App, retErr error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	logger, err := initLogger(cfg.LogLevel)
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

	pluginManager := plugin.NewManager(coreDeps.router)
	pluginManager.SetHookRegistrar(tgRuntime.dispatcher)
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
		coreDeps.taskManager,
		coreDeps.jobsManager,
	)
	if domServices.schedEngine != nil {
		pluginManager.SetSchedulerCleaner(domServices.schedEngine)
	}
	if domServices.addonManager != nil {
		domServices.addonManager.SetProcessManager(coreDeps.procManager)
	}
	if tgRuntime.assistant != nil {
		tgRuntime.assistant.SetCoreRouter(coreDeps.router)
		tgRuntime.assistant.SetSettingsService(domServices.settingsService)
		tgRuntime.assistant.SetMetricsCollector(coreDeps.metrics)
	}

	if err := migrateBuiltinFeatures(context.Background(), coreDeps.db); err != nil {
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
			Gate:      coreDeps.capGate,
			Network:   coreDeps.netService,
			Process:   coreDeps.procManager,
			Files:     coreDeps.fsManager,
			Secrets:   coreDeps.secretManager,
			Audit:     coreDeps.auditService,
			Resources: coreDeps.resourceManager,
			Workers:   coreDeps.workerManager,
			Tasks:     coreDeps.taskManager,
			Jobs:      coreDeps.jobsManager,
		},
	}
	if err := registerBuiltinModules(context.Background(), featureRuntime); err != nil {
		return nil, err
	}

	rt := runtime.New()
	if err := rt.Register(&infrastructureComponent{core: coreDeps, domain: domServices}); err != nil {
		return nil, fmt.Errorf("register infrastructure component: %w", err)
	}
	if err := rt.Register(dependencyComponent{Component: coreDeps.eventBus, dependencies: []string{"infrastructure"}}); err != nil {
		return nil, fmt.Errorf("register eventbus component: %w", err)
	}
	if err := rt.Register(dependencyComponent{Component: coreDeps.workerManager, dependencies: []string{"infrastructure"}}); err != nil {
		return nil, fmt.Errorf("register workers component: %w", err)
	}
	if err := rt.Register(coreDeps.jobsManager); err != nil {
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
	if err := rt.Register(tgRuntime.dispatcher); err != nil {
		return nil, fmt.Errorf("register dispatcher component: %w", err)
	}
	if tgRuntime.assistant != nil {
		if err := rt.Register(tgRuntime.assistant); err != nil {
			return nil, fmt.Errorf("register assistant component: %w", err)
		}
	}
	if err := rt.Register(pluginManager); err != nil {
		return nil, fmt.Errorf("register plugins component: %w", err)
	}

	return &App{
		cfg:             cfg,
		logger:          logger,
		db:              coreDeps.db,
		client:          tgRuntime.client,
		plugins:         pluginManager,
		router:          coreDeps.router,
		sched:           domServices.schedEngine,
		eventBus:        coreDeps.eventBus,
		assistant:       tgRuntime.assistant,
		limiter:         coreDeps.cmdLimiter,
		interLimiter:    coreDeps.interLimiter,
		addonMgr:        domServices.addonManager,
		callbackStore:   coreDeps.callbackStore,
		inlineEngine:    coreDeps.inlineEngine,
		settingsService: domServices.settingsService,
		media:           domServices.mediaService,
		processRunner:   domServices.processRunner,
		startTime:       domServices.startTime,
		workers:         coreDeps.workerManager,
		tasks:           coreDeps.taskManager,
		jobs:            coreDeps.jobsManager,
		resources:       coreDeps.resourceManager,
		idemp:           coreDeps.idempManager,
		runtime:         rt,
		shutdownDone:    make(chan struct{}),
	}, nil
}

func (a *App) Run(ctx context.Context) error { return a.runLifecycle(ctx) }

func initLogger(logLevel string) (*zap.Logger, error) {
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
	zapCfg := zap.NewDevelopmentConfig()
	zapCfg.Level = zap.NewAtomicLevelAt(zapLevel)
	return zapCfg.Build()
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
