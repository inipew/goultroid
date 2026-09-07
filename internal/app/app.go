package app

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/addon"
	appCap "github.com/inipew/goultroid/internal/application/capability"
	appCmd "github.com/inipew/goultroid/internal/application/command"
	"github.com/inipew/goultroid/internal/assistant"
	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/scheduler"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/ratelimit"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/telegram"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// App is the central root composition container coordinating all GoUltroid subsystems.
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
	addonMgr        *addon.Manager
	callbackStore   *callback.StateStore
	inlineEngine    *inline.Engine
	settingsService *settings.Service
}

// New initializes the full GoUltroid application stack through layered dependency injection.
func New(cfg *config.Config) (*App, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	logger, err := initLogger(cfg.LogLevel)
	if err != nil {
		return nil, fmt.Errorf("failed to build logger: %w", err)
	}

	// 1. Build core storage, routing, permission, and messaging bus
	coreDeps, err := buildCore(cfg, logger)
	if err != nil {
		return nil, err
	}

	// 2. Build Telegram network client and dispatcher runtime
	tgRuntime, err := buildTelegramRuntime(cfg, coreDeps, logger)
	if err != nil {
		return nil, err
	}

	// 3. Build modular domain services
	domServices, err := buildDomainServices(cfg, coreDeps, tgRuntime, logger)
	if err != nil {
		return nil, err
	}

	// 4. Build and register plugins
	pluginManager := plugin.NewManager(coreDeps.router)
	pluginManager.SetHookRegistrar(tgRuntime.dispatcher)

	capRegistry := appCap.NewRegistry()
	pluginManager.SetCapabilityRegistry(capRegistry)

	unifiedCmdReg := appCmd.NewUnifiedRegistry()
	pluginManager.SetUnifiedCommandRegistry(unifiedCmdReg)

	if tgRuntime.assistant != nil {
		tgRuntime.assistant.SetUnifiedRegistry(unifiedCmdReg)
	}

	pluginsList, err := buildPlugins(coreDeps, tgRuntime, domServices)
	if err != nil {
		_ = coreDeps.eventBus.Close()
		_ = coreDeps.db.Close()
		return nil, err
	}

	for _, p := range pluginsList {
		if err := pluginManager.Register(p); err != nil {
			_ = coreDeps.eventBus.Close()
			_ = coreDeps.db.Close()
			return nil, fmt.Errorf("failed to register plugin %q: %w", p.Name(), err)
		}
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
		addonMgr:        domServices.addonManager,
		callbackStore:   coreDeps.callbackStore,
		inlineEngine:    coreDeps.inlineEngine,
		settingsService: domServices.settingsService,
	}, nil
}

// Run starts the application and coordinates background worker lifecycles.
func (a *App) Run(ctx context.Context) error {
	return a.runLifecycle(ctx)
}

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
