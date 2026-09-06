package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/inipew/goultroid/internal/addon"
	"github.com/inipew/goultroid/internal/assistant"
	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/scheduler"
	broadcastSvc "github.com/inipew/goultroid/internal/services/broadcast"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/localization"
	mediaSvc "github.com/inipew/goultroid/internal/services/media"
	"github.com/inipew/goultroid/internal/services/moderation"
	pmpermitSvc "github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/services/ratelimit"
	"github.com/inipew/goultroid/internal/services/storage"
	userlogSvc "github.com/inipew/goultroid/internal/services/userlog"
	"github.com/inipew/goultroid/internal/telegram"
	addonPluginPkg "github.com/inipew/goultroid/plugins/addon"
	"github.com/inipew/goultroid/plugins/admin"
	"github.com/inipew/goultroid/plugins/afk"
	"github.com/inipew/goultroid/plugins/alive"
	"github.com/inipew/goultroid/plugins/blacklist"
	"github.com/inipew/goultroid/plugins/broadcast"
	"github.com/inipew/goultroid/plugins/downloader"
	"github.com/inipew/goultroid/plugins/filters"
	"github.com/inipew/goultroid/plugins/forward"
	"github.com/inipew/goultroid/plugins/fun"
	"github.com/inipew/goultroid/plugins/help"
	"github.com/inipew/goultroid/plugins/info"
	"github.com/inipew/goultroid/plugins/locks"
	"github.com/inipew/goultroid/plugins/media"
	"github.com/inipew/goultroid/plugins/notes"
	"github.com/inipew/goultroid/plugins/pin"
	"github.com/inipew/goultroid/plugins/ping"
	"github.com/inipew/goultroid/plugins/pmpermit"
	"github.com/inipew/goultroid/plugins/profile"
	schedPlugin "github.com/inipew/goultroid/plugins/scheduler"
	"github.com/inipew/goultroid/plugins/sticker"
	"github.com/inipew/goultroid/plugins/sudo"
	"github.com/inipew/goultroid/plugins/system"
	"github.com/inipew/goultroid/plugins/userlog"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type App struct {
	cfg           *config.Config
	logger        *zap.Logger
	db            *database.DB
	client        *telegram.Client
	plugins       *plugin.Manager
	router        *core.Router
	sched         *scheduler.Engine
	eventBus      *core.EventBus
	assistant     assistant.Client
	limiter       *ratelimit.Limiter
	addonMgr      *addon.Manager
	callbackStore *callback.StateStore
	inlineEngine  *inline.Engine
}

func New(cfg *config.Config) (*App, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	var zapLevel zapcore.Level
	switch cfg.LogLevel {
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
	logger, err := zapCfg.Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build logger: %w", err)
	}

	dbPath := cfg.DatabasePath
	if dbPath == "" {
		dbPath = "data/goultroid.db"
	}
	db, err := database.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	perms := core.NewPermissions(cfg.OwnerID, cfg.SudoUsers)
	dbSudos, err := db.GetSudoUsers(context.Background())
	if err != nil {
		logger.Warn("failed to load sudo users from db", zap.Error(err))
	} else {
		for _, u := range dbSudos {
			perms.AddSudo(u.UserID)
		}
		logger.Info("loaded dynamic sudo users", zap.Int("count", len(dbSudos)))
	}

	router := core.NewRouter(cfg.Prefix)
	mgr := plugin.NewManager(router)
	dispatcher := telegram.NewDispatcher(router, perms, nil, logger)
	eventBus := core.NewEventBus()
	dispatcher.SetEventBus(eventBus)

	client, err := telegram.NewClient(cfg, dispatcher, db, logger)
	if err != nil {
		_ = eventBus.Close()
		_ = db.Close()
		return nil, fmt.Errorf("failed to create telegram client: %w", err)
	}

	afkPlugin := afk.New(db, cfg.OwnerID, client.Service)
	dispatcher.AddPrioritizedMessageHandler(telegram.PriorityFeature, afkPlugin.HandleIncomingMessage)
	filtersPlugin := filters.New(db, client.Service)
	dispatcher.AddPrioritizedMessageHandler(telegram.PriorityModeration, filtersPlugin.HandleIncomingMessage)
	blacklistPlugin := blacklist.New(db, client.Service)
	dispatcher.AddPrioritizedMessageHandler(telegram.PrioritySecurity, blacklistPlugin.HandleIncomingMessage)

	metrics := core.NewDefaultMetricsTracker()
	dispatcher.Executor().SetMetrics(metrics)
	localizer := localization.New("id")
	dispatcher.SetLocalizer(localizer)

	callbackStore := callback.NewStateStore()
	callbackRouter := callback.NewRouter(logger, callbackStore)
	callbackRouter.SetMetrics(metrics)
	// Fase 4: dedicated limiter for callbacks (30/min, burst 10) separate from commands
	interactionLimiter := ratelimit.New(ratelimit.Policy{Limit: 30, Window: time.Minute, Burst: 10}, 5*time.Minute)
	callbackRouter.SetLimiter(interactionLimiter)
	callbackRouter.SetTimeout(15 * time.Second)
	dispatcher.SetCallbackRouter(callbackRouter)
	// lifecycle for expired callback buttons (shutdown-aware: tied to background context, Stop() on App shutdown)
	callbackStore.Start(context.Background())

	inlineRegistry := inline.NewRegistry()
	_ = inlineRegistry.Register(&defaultCatchAllInlineHandler{router: router, startTime: time.Now()})
	_ = inlineRegistry.Register(&defaultHelpInlineHandler{router: router})
	_ = inlineRegistry.Register(&defaultPingInlineHandler{startTime: time.Now()})
	inlineEngine := inline.NewEngine(inlineRegistry, logger)
	inlineEngine.SetMetrics(metrics)
	inlineEngine.SetLimiter(interactionLimiter)
	inlineEngine.SetTimeout(4 * time.Second)
	inlineEngine.SetPermissions(perms)
	dispatcher.SetInlineEngine(inlineEngine)
	inlineEngine.Cache().Start(context.Background())

	modService := moderation.NewService(db, client.Service, logger)
	schedEngine := scheduler.NewEngine(db, client.Service, router, perms, logger)
	schedEngine.SetExecutor(dispatcher.Executor())

	systemPlugin := system.New()
	systemPlugin.SetMetrics(metrics)

	storageDir := filepath.Join("data", "storage")
	fileStorage, err := storage.NewFileStorage(storageDir, 10*1024*1024*1024)
	var appStorage storage.Storage = fileStorage
	if err != nil {
		logger.Warn("failed to initialize persistent file storage, falling back to memory", zap.Error(err))
		appStorage = storage.NewMemoryStorage()
	}

	processRunner := process.NewOSRunner(3, 5*time.Minute, 4*1024*1024)
	downloadRegistry := download.NewRegistry(
		download.NewExtractorProvider(processRunner, 500*1024*1024),
		download.NewDirectHTTPProvider(5*time.Minute, 500*1024*1024),
	)
	mediaGuard := mediaSvc.NewResourceGuard(2, 100*1024*1024)
	mediaService := mediaSvc.NewService(processRunner, appStorage, mediaGuard)
	downloaderPlugin := downloader.New(downloadRegistry, appStorage)
	mediaPlugin := media.New(mediaService)

	pmpermitService := pmpermitSvc.NewService(db, client.Service, cfg.OwnerID, perms, logger)
	pmpermitService.SetEventBus(eventBus)
	broadcastService := broadcastSvc.NewService(client.Service, logger)
	userlogService := userlogSvc.NewService(db, client.Service, logger)
	pmpermitPlugin := pmpermit.New(pmpermitService)
	dispatcher.AddPrioritizedMessageHandler(telegram.PrioritySecurity, pmpermitPlugin.HandleIncomingMessage)
	userlogPlugin := userlog.New(userlogService, cfg.OwnerID)
	userlogPlugin.SetEventBus(eventBus)
	dispatcher.AddPrioritizedMessageHandler(telegram.PriorityObservability, userlogPlugin.HandleIncomingMessage)
	broadcastPlugin := broadcast.New(broadcastService)

	limiter := ratelimit.New(ratelimit.Policy{Limit: 60, Window: time.Minute, Burst: 30}, 5*time.Minute)
	dispatcher.Executor().SetRateLimiter(commandRateLimiterAdapter{limiter: limiter})

	// Voice chat is intentionally not wired until a real Telegram VC transport exists.

	addonGate := addon.NewCapabilityGate()
	addonManager := addon.NewManager(db, addonGate, "1.0.0", logger)
	_ = addonManager.LoadInstalled(context.Background())
	addonPlugin := addonPluginPkg.New(addonManager)

	var assistantClient assistant.Client
	if cfg.BotToken != "" {
		bot := assistant.NewBotClient(cfg.AppID, cfg.AppHash, cfg.BotToken, logger)
		bot.SetCallbackRouter(callbackRouter)
		bot.SetInlineEngine(inlineEngine)
		bot.SetLocalizer(localizer)
		assistantClient = bot
	}

	plugins := []plugin.Plugin{
		ping.New(),
		help.New(router),
		alive.New(time.Now()),
		pin.New(),
		forward.New(),
		downloaderPlugin,
		sudo.New(db, perms),
		notes.New(db),
		afkPlugin,
		admin.New(modService),
		mediaPlugin,
		sticker.New(),
		info.New(),
		systemPlugin,
		filtersPlugin,
		fun.New(),
		schedPlugin.New(schedEngine),
		locks.New(),
		blacklistPlugin,
		profile.New(),
		pmpermitPlugin,
		broadcastPlugin,
		userlogPlugin,
		addonPlugin,
	}

	for _, p := range plugins {
		if err := mgr.Register(p); err != nil {
			_ = eventBus.Close()
			_ = db.Close()
			return nil, fmt.Errorf("failed to register plugin %q: %w", p.Name(), err)
		}
	}

	return &App{
		cfg: cfg, logger: logger, db: db, client: client, plugins: mgr,
		router: router, sched: schedEngine, eventBus: eventBus,
		assistant: assistantClient, limiter: limiter, addonMgr: addonManager,
		callbackStore: callbackStore, inlineEngine: inlineEngine,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	a.logger.Info("starting GoUltroid...")
	// Fase 4: shutdown-aware cleanup for interaction state
	if a.callbackStore != nil {
		defer a.callbackStore.Stop()
	}
	if a.inlineEngine != nil && a.inlineEngine.Cache() != nil {
		defer a.inlineEngine.Cache().Stop()
		defer func() { _ = a.inlineEngine.Cache().Prune() }()
	}
	if a.client != nil && a.client.Dispatcher() != nil {
		a.client.Dispatcher().SetRootContext(ctx)
		a.client.Dispatcher().Start(ctx)
	}
	// P1-01: Defer scheduler start until Telegram service is ready to avoid
	// claiming jobs before the MTProto session is authenticated.
	if a.sched != nil {
		if a.client != nil && a.client.Ready() != nil {
			go func() {
				select {
				case <-ctx.Done():
					return
				case <-a.client.Ready():
				}
				if err := a.sched.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
					a.logger.Warn("scheduler failed to start after readiness gate", zap.Error(err))
				} else {
					a.logger.Info("scheduler engine started after Telegram readiness gate")
				}
			}()
		} else {
			if err := a.sched.Start(ctx); err != nil {
				return fmt.Errorf("failed to start scheduler engine: %w", err)
			}
		}
	}
	if a.assistant != nil {
		go func() {
			if err := a.assistant.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.logger.Warn("assistant bot stopped with error", zap.Error(err))
			}
		}()
	}
	return a.client.Run(ctx)
}
