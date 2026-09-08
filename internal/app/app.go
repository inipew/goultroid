package app

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/addon"
	"github.com/inipew/goultroid/internal/assistant"
	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
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

func New(cfg *config.Config) (*App, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	logger, err := initLogger(cfg.LogLevel)
	if err != nil {
		return nil, fmt.Errorf("failed to build logger: %w", err)
	}
	coreDeps, err := buildCore(cfg, logger)
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
	if tgRuntime.assistant != nil {
		tgRuntime.assistant.SetCoreRouter(coreDeps.router)
		tgRuntime.assistant.SetSettingsService(domServices.settingsService)
		tgRuntime.assistant.SetMetricsCollector(coreDeps.metrics)
	}

	if err := migrateBuiltinFeatures(context.Background(), coreDeps.db); err != nil {
		_ = coreDeps.eventBus.Close()
		_ = coreDeps.db.Close()
		return nil, err
	}

	featureRuntime := &module.Runtime{
		OwnerID: coreDeps.perms.OwnerID,
		Logger:  domServices.logger,
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
			ModService:       domServices.modService,
			PMPermitService:  domServices.pmpermitService,
			BroadcastService: domServices.broadcastService,
			UserlogService:   domServices.userlogService,
			AddonManager:     domServices.addonManager,
			SettingsService:  domServices.settingsService,
			SchedEngine:      domServices.schedEngine,
		},
	}
	if err := registerBuiltinModules(context.Background(), featureRuntime); err != nil {
		_ = coreDeps.eventBus.Close()
		_ = coreDeps.db.Close()
		return nil, err
	}

	return &App{cfg: cfg, logger: logger, db: coreDeps.db, client: tgRuntime.client, plugins: pluginManager, router: coreDeps.router, sched: domServices.schedEngine, eventBus: coreDeps.eventBus, assistant: tgRuntime.assistant, limiter: tgRuntime.cmdLimiter, addonMgr: domServices.addonManager, callbackStore: coreDeps.callbackStore, inlineEngine: coreDeps.inlineEngine, settingsService: domServices.settingsService}, nil
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
