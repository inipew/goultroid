package app

import (
	"context"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/scheduler"
	"github.com/inipew/goultroid/internal/telegram"
	"github.com/inipew/goultroid/plugins/admin"
	"github.com/inipew/goultroid/plugins/afk"
	"github.com/inipew/goultroid/plugins/alive"
	"github.com/inipew/goultroid/plugins/blacklist"
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
	schedPlugin "github.com/inipew/goultroid/plugins/scheduler"
	"github.com/inipew/goultroid/plugins/sticker"
	"github.com/inipew/goultroid/plugins/sudo"
	"github.com/inipew/goultroid/plugins/system"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// App coordinates the configuration, database, core router, plugins, and telegram client.
type App struct {
	cfg     *config.Config
	logger  *zap.Logger
	db      *database.DB
	client  *telegram.Client
	plugins *plugin.Manager
	router  *core.Router
	sched   *scheduler.Engine
}

// New constructs and wires all application components.
func New(cfg *config.Config) (*App, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	// Setup structured logger
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

	// Setup SQLite Database
	dbPath := cfg.DatabasePath
	if dbPath == "" {
		dbPath = "data/goultroid.db"
	}
	db, err := database.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	// Core permissions & router
	perms := core.NewPermissions(cfg.OwnerID, cfg.SudoUsers)

	// Synchronize dynamic sudo users from database into permissions
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

	// Plugin manager
	mgr := plugin.NewManager(router)

	// Telegram dispatcher & client
	dispatcher := telegram.NewDispatcher(router, perms, nil, logger)

	// Domain event bus — allows plugins to react to edit/delete/create events.
	eventBus := core.NewEventBus()
	dispatcher.SetEventBus(eventBus)

	client, err := telegram.NewClient(cfg, dispatcher, db, logger)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to create telegram client: %w", err)
	}

	// Plugins
	afkPlugin := afk.New(db, cfg.OwnerID, client.Service)
	dispatcher.AddMessageHandler(afkPlugin.HandleIncomingMessage)

	filtersPlugin := filters.New(db, client.Service)
	dispatcher.AddMessageHandler(filtersPlugin.HandleIncomingMessage)

	blacklistPlugin := blacklist.New(db, client.Service)
	dispatcher.AddMessageHandler(blacklistPlugin.HandleIncomingMessage)

	// Scheduler Engine (shares unified CommandExecutor with Dispatcher)
	schedEngine := scheduler.NewEngine(db, client.Service, router, perms, logger)
	schedEngine.SetExecutor(dispatcher.Executor())

	plugins := []plugin.Plugin{
		ping.New(),
		help.New(router),
		alive.New(time.Now()),
		pin.New(),
		forward.New(),
		downloader.New(),
		sudo.New(db, perms),
		notes.New(db),
		afkPlugin,
		admin.New(),
		media.New(),
		sticker.New(),
		info.New(),
		system.New(),
		filtersPlugin,
		fun.New(),
		schedPlugin.New(schedEngine),
		locks.New(),
		blacklistPlugin,
	}

	for _, p := range plugins {
		if err := mgr.Register(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("failed to register plugin %q: %w", p.Name(), err)
		}
	}

	return &App{
		cfg:     cfg,
		logger:  logger,
		db:      db,
		client:  client,
		plugins: mgr,
		router:  router,
		sched:   schedEngine,
	}, nil
}

// Run connects to Telegram and maintains the update loop until context is canceled.
func (a *App) Run(ctx context.Context) error {
	a.logger.Info("starting GoUltroid...")

	// Pass application root context to dispatcher for command lifetime scoping
	if a.client != nil && a.client.Dispatcher() != nil {
		a.client.Dispatcher().SetRootContext(ctx)
	}

	// Start scheduler engine with application root context
	if a.sched != nil {
		if err := a.sched.Start(ctx); err != nil {
			return fmt.Errorf("failed to start scheduler engine: %w", err)
		}
	}

	return a.client.Run(ctx)
}

// Shutdown triggers graceful shutdown of all registered plugins, scheduler, and flushes logs.
func (a *App) Shutdown() error {
	a.logger.Info("shutting down GoUltroid...")
	if err := a.plugins.Shutdown(); err != nil {
		a.logger.Warn("error during plugin shutdown", zap.Error(err))
	}
	if a.sched != nil {
		if err := a.sched.Stop(); err != nil {
			a.logger.Warn("error stopping scheduler engine", zap.Error(err))
		}
	}
	if a.db != nil {
		if err := a.db.Close(); err != nil {
			a.logger.Warn("error closing database", zap.Error(err))
		}
	}
	_ = a.logger.Sync()
	return nil
}
