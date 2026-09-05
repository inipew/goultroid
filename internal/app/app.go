package app

import (
	"context"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/telegram"
	"github.com/inipew/goultroid/plugins/alive"
	"github.com/inipew/goultroid/plugins/downloader"
	"github.com/inipew/goultroid/plugins/forward"
	"github.com/inipew/goultroid/plugins/help"
	"github.com/inipew/goultroid/plugins/pin"
	"github.com/inipew/goultroid/plugins/ping"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// App coordinates the configuration, core router, plugins, and telegram client.
type App struct {
	cfg     *config.Config
	logger  *zap.Logger
	client  *telegram.Client
	plugins *plugin.Manager
	router  *core.Router
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

	// Core permissions & router
	perms := core.NewPermissions(cfg.OwnerID, cfg.SudoUsers)
	router := core.NewRouter(cfg.Prefix)

	// Plugin manager
	mgr := plugin.NewManager(router)

	// Telegram dispatcher & client
	dispatcher := telegram.NewDispatcher(router, perms, nil, logger)
	client, err := telegram.NewClient(cfg, dispatcher, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram client: %w", err)
	}

	// Register default plugins
	if err := mgr.Register(ping.New()); err != nil {
		return nil, fmt.Errorf("failed to register ping plugin: %w", err)
	}
	if err := mgr.Register(help.New(router)); err != nil {
		return nil, fmt.Errorf("failed to register help plugin: %w", err)
	}
	if err := mgr.Register(alive.New(time.Now())); err != nil {
		return nil, fmt.Errorf("failed to register alive plugin: %w", err)
	}
	if err := mgr.Register(pin.New()); err != nil {
		return nil, fmt.Errorf("failed to register pin plugin: %w", err)
	}
	if err := mgr.Register(forward.New()); err != nil {
		return nil, fmt.Errorf("failed to register forward plugin: %w", err)
	}
	if err := mgr.Register(downloader.New()); err != nil {
		return nil, fmt.Errorf("failed to register downloader plugin: %w", err)
	}

	return &App{
		cfg:     cfg,
		logger:  logger,
		client:  client,
		plugins: mgr,
		router:  router,
	}, nil
}

// Run connects to Telegram and maintains the update loop until context is canceled.
func (a *App) Run(ctx context.Context) error {
	a.logger.Info("starting GoUltroid...")
	return a.client.Run(ctx)
}

// Shutdown triggers graceful shutdown of all registered plugins and flushes logs.
func (a *App) Shutdown() error {
	a.logger.Info("shutting down GoUltroid...")
	if err := a.plugins.Shutdown(); err != nil {
		a.logger.Warn("error during plugin shutdown", zap.Error(err))
	}
	_ = a.logger.Sync()
	return nil
}
