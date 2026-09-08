package app

import (
	"context"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/localization"
	"github.com/inipew/goultroid/internal/services/ratelimit"
	"github.com/inipew/goultroid/plugins/sudo"
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
	sudoCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	sudoRepo := sudo.NewSQLiteRepository(db)
	dbSudos, err := sudoRepo.GetSudoUsers(sudoCtx)
	cancel()
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

	cmdLimiter := ratelimit.New(ratelimit.Policy{Limit: 60, Window: time.Minute, Burst: 30}, 5*time.Minute)
	interLimiter := ratelimit.New(ratelimit.Policy{Limit: 30, Window: time.Minute, Burst: 10}, 5*time.Minute)

	callbackStore := callback.NewStateStore()
	callbackRouter := callback.NewRouter(logger, callbackStore)
	callbackRouter.SetMetrics(metrics)
	callbackRouter.SetLimiter(interLimiter)
	callbackRouter.SetTimeout(15 * time.Second)

	inlineRegistry := inline.NewRegistry()
	if err := inlineRegistry.Register(&defaultCatchAllInlineHandler{router: router, startTime: time.Now()}); err != nil {
		cleanupCore(&coreDependencies{db: db, eventBus: eventBus}, logger)
		return nil, fmt.Errorf("register catch-all inline handler: %w", err)
	}
	if err := inlineRegistry.Register(&defaultHelpInlineHandler{router: router}); err != nil {
		cleanupCore(&coreDependencies{db: db, eventBus: eventBus}, logger)
		return nil, fmt.Errorf("register help inline handler: %w", err)
	}
	if err := inlineRegistry.Register(&defaultPingInlineHandler{startTime: time.Now()}); err != nil {
		cleanupCore(&coreDependencies{db: db, eventBus: eventBus}, logger)
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
