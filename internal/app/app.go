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

	// Unified operational metrics tracker
	metrics := core.NewDefaultMetricsTracker()
	dispatcher.Executor().SetMetrics(metrics)

	// Scheduler Engine (shares unified CommandExecutor with Dispatcher)
	schedEngine := scheduler.NewEngine(db, client.Service, router, perms, logger)
	schedEngine.SetExecutor(dispatcher.Executor())

	systemPlugin := system.New()
	systemPlugin.SetMetrics(metrics)

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
		systemPlugin,
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
// ctx is the global shutdown budget (expected 30s from caller). Scheduler gets a 10s slice of that budget.
// Lifecycle boundary is caller-controlled; no Background() is created inside.
func (a *App) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	a.logger.Info("shutting down GoUltroid...", zap.Duration("budget", func() time.Duration { if d, ok := ctx.Deadline(); ok { return time.Until(d) }; return 0 }()))

	// 1. Stop scheduler engine first with 10s slice of global budget
	if a.sched != nil {
		schedCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := a.sched.StopContext(schedCtx); err != nil {
			a.logger.Warn("error stopping scheduler engine", zap.Error(err))
		}
		cancel()
		// If global budget already exceeded, abort early
		select {
		case <-ctx.Done():
			a.logger.Warn("global shutdown budget exceeded after scheduler stop", zap.Error(ctx.Err()))
			if a.db != nil {
				_ = a.db.Close()
			}
			_ = a.logger.Sync()
			return ctx.Err()
		default:
		}
	}

	// 2. Stop registered plugins with remaining budget
	if err := a.plugins.ShutdownWithContext(ctx); err != nil {
		a.logger.Warn("error during plugin shutdown", zap.Error(err))
	}

	// 3. Close database last so running goroutines never write to a closed DB connection
	if a.db != nil {
		if err := a.db.Close(); err != nil {
			a.logger.Warn("error closing database", zap.Error(err))
		}
	}
	_ = a.logger.Sync()
	return nil
}
