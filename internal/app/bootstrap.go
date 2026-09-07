package app

import (
	"context"
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
	"github.com/inipew/goultroid/internal/settings"
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
	settingsPluginPkg "github.com/inipew/goultroid/plugins/settings"
	"github.com/inipew/goultroid/plugins/sticker"
	"github.com/inipew/goultroid/plugins/sudo"
	"github.com/inipew/goultroid/plugins/system"
	"github.com/inipew/goultroid/plugins/userlog"
	"go.uber.org/zap"
)

func buildCore(cfg *config.Config, logger *zap.Logger) (*coreDependencies, error) {
	dbPath := cfg.DatabasePath
	if dbPath == "" {
		dbPath = "data/goultroid.db"
	}
	db, err := database.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("initialize database: %w", err)
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
	eventBus := core.NewEventBus()
	metrics := core.NewDefaultMetricsTracker()
	localizer := localization.New("id")

	// Limiters
	cmdLimiter := ratelimit.New(ratelimit.Policy{Limit: 60, Window: time.Minute, Burst: 30}, 5*time.Minute)
	interLimiter := ratelimit.New(ratelimit.Policy{Limit: 30, Window: time.Minute, Burst: 10}, 5*time.Minute)

	// Callback Router & StateStore
	callbackStore := callback.NewStateStore()
	callbackRouter := callback.NewRouter(logger, callbackStore)
	callbackRouter.SetMetrics(metrics)
	callbackRouter.SetLimiter(interLimiter)
	callbackRouter.SetTimeout(15 * time.Second)

	// Inline Engine & Registry
	inlineRegistry := inline.NewRegistry()
	if err := inlineRegistry.Register(&defaultCatchAllInlineHandler{router: router, startTime: time.Now()}); err != nil {
		_ = eventBus.Close()
		_ = db.Close()
		return nil, fmt.Errorf("register catch-all inline handler: %w", err)
	}
	if err := inlineRegistry.Register(&defaultHelpInlineHandler{router: router}); err != nil {
		_ = eventBus.Close()
		_ = db.Close()
		return nil, fmt.Errorf("register help inline handler: %w", err)
	}
	if err := inlineRegistry.Register(&defaultPingInlineHandler{startTime: time.Now()}); err != nil {
		_ = eventBus.Close()
		_ = db.Close()
		return nil, fmt.Errorf("register ping inline handler: %w", err)
	}

	inlineEngine := inline.NewEngine(inlineRegistry, logger)
	inlineEngine.SetMetrics(metrics)
	inlineEngine.SetLimiter(interLimiter)
	inlineEngine.SetTimeout(4 * time.Second)
	inlineEngine.SetPermissions(perms)

	return &coreDependencies{
		db:             db,
		perms:          perms,
		router:         router,
		eventBus:       eventBus,
		metrics:        metrics,
		localizer:      localizer,
		callbackStore:  callbackStore,
		callbackRouter: callbackRouter,
		inlineEngine:   inlineEngine,
		cmdLimiter:     cmdLimiter,
		interLimiter:   interLimiter,
	}, nil
}

func buildTelegramRuntime(cfg *config.Config, core *coreDependencies, logger *zap.Logger) (*telegramRuntime, error) {
	dispatcherDeps := telegram.DispatcherDeps{
		Router:         core.router,
		Permissions:    core.perms,
		Service:        nil,
		Logger:         logger,
		EventBus:       core.eventBus,
		Localizer:      core.localizer,
		CallbackRouter: core.callbackRouter,
		InlineEngine:   core.inlineEngine,
	}
	dispatcher := telegram.NewDispatcherWithDeps(dispatcherDeps)
	dispatcher.Executor().SetMetrics(core.metrics)
	dispatcher.Executor().SetRateLimiter(commandRateLimiterAdapter{limiter: core.cmdLimiter})

	client, err := telegram.NewClient(cfg, dispatcher, core.db, logger)
	if err != nil {
		_ = core.eventBus.Close()
		_ = core.db.Close()
		return nil, fmt.Errorf("create telegram client: %w", err)
	}

	var assistantClient assistant.Client
	if cfg.BotToken != "" {
		bot := assistant.NewBotClient(cfg.AppID, cfg.AppHash, cfg.BotToken, logger)
		bot.SetCallbackRouter(core.callbackRouter)
		bot.SetInlineEngine(core.inlineEngine)
		bot.SetLocalizer(core.localizer)
		assistantClient = bot
	}

	return &telegramRuntime{
		client:     client,
		dispatcher: dispatcher,
		assistant:  assistantClient,
	}, nil
}

func buildDomainServices(cfg *config.Config, core *coreDependencies, tg *telegramRuntime, logger *zap.Logger) (*domainServices, error) {
	startTime := time.Now()

	settingsRegistry := settings.NewRegistry()
	if err := settings.RegisterDefaultDefinitions(settingsRegistry); err != nil {
		_ = core.eventBus.Close()
		_ = core.db.Close()
		return nil, fmt.Errorf("register default settings definitions: %w", err)
	}
	settingsService := settings.NewService(core.db, settingsRegistry, core.eventBus)

	modService := moderation.NewService(core.db, tg.client.Service, logger)
	schedEngine := scheduler.NewEngine(core.db, tg.client.Service, core.router, core.perms, logger)
	schedEngine.SetExecutor(tg.dispatcher.Executor())

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

	pmpermitService := pmpermitSvc.NewService(core.db, tg.client.Service, cfg.OwnerID, core.perms, logger)
	pmpermitService.SetEventBus(core.eventBus)

	broadcastService := broadcastSvc.NewService(tg.client.Service, logger)
	userlogService := userlogSvc.NewService(core.db, tg.client.Service, logger)

	addonGate := addon.NewCapabilityGate()
	addonManager := addon.NewManager(core.db, addonGate, "1.0.0", logger)
	if err := addonManager.LoadInstalled(context.Background()); err != nil {
		logger.Warn("failed to load installed addons", zap.Error(err))
	}

	return &domainServices{
		settingsService:  settingsService,
		settingsRegistry: settingsRegistry,
		modService:       modService,
		schedEngine:      schedEngine,
		storage:          appStorage,
		mediaService:     mediaService,
		downloadRegistry: downloadRegistry,
		pmpermitService:  pmpermitService,
		broadcastService: broadcastService,
		userlogService:   userlogService,
		addonManager:     addonManager,
		startTime:        startTime,
		logger:           logger,
	}, nil
}

func buildPlugins(core *coreDependencies, tg *telegramRuntime, dom *domainServices) ([]plugin.Plugin, error) {
	afkPlugin := afk.New(core.db, core.perms.OwnerID, tg.client.Service)
	afkPlugin.SetLogger(dom.logger)
	afkPlugin.SetResolver(tg.dispatcher.Resolver())

	filtersPlugin := filters.New(core.db, tg.client.Service)
	blacklistPlugin := blacklist.New(core.db, tg.client.Service)

	systemPlugin := system.New()
	systemPlugin.SetMetrics(core.metrics)

	downloaderPlugin := downloader.New(dom.downloadRegistry, dom.storage)
	mediaPlugin := media.New(dom.mediaService)

	pmpermitPlugin := pmpermit.New(dom.pmpermitService)
	userlogPlugin := userlog.New(dom.userlogService, core.perms.OwnerID)
	userlogPlugin.SetEventBus(core.eventBus)
	broadcastPlugin := broadcast.New(dom.broadcastService)
	addonPlugin := addonPluginPkg.New(dom.addonManager)

	helpPlugin := help.New(core.router)
	helpPlugin.SetStateStore(core.callbackStore)
	if err := core.callbackRouter.Register(helpPlugin); err != nil {
		return nil, fmt.Errorf("register help callback handler: %w", err)
	}

	settingsPlugin := settingsPluginPkg.New(dom.settingsService, core.callbackStore)
	settingsPlugin.SetLogger(dom.logger)
	if err := core.callbackRouter.Register(settingsPlugin); err != nil {
		return nil, fmt.Errorf("register settings callback handler: %w", err)
	}

	return []plugin.Plugin{
		ping.New(),
		helpPlugin,
		alive.New(dom.startTime),
		pin.New(),
		forward.New(),
		downloaderPlugin,
		sudo.New(core.db, core.perms),
		notes.New(core.db),
		afkPlugin,
		admin.New(dom.modService),
		mediaPlugin,
		sticker.New(),
		info.New(),
		systemPlugin,
		filtersPlugin,
		fun.New(),
		schedPlugin.New(dom.schedEngine),
		locks.New(),
		blacklistPlugin,
		profile.New(),
		pmpermitPlugin,
		broadcastPlugin,
		userlogPlugin,
		addonPlugin,
		settingsPlugin,
	}, nil
}
