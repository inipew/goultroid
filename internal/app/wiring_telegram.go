package app

import (
	"fmt"

	"github.com/inipew/goultroid/internal/assistant"
	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/telegram"
	"go.uber.org/zap"
)

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
	dispatcher, err := telegram.NewDispatcherWithDeps(dispatcherDeps)
	if err != nil {
		cleanupCore(core, logger)
		return nil, fmt.Errorf("create dispatcher: %w", err)
	}
	dispatcher.Executor().SetMetrics(core.metrics)
	dispatcher.Executor().SetRateLimiter(commandRateLimiterAdapter{limiter: core.cmdLimiter})

	client, err := telegram.NewClient(cfg, dispatcher, core.db, logger)
	if err != nil {
		cleanupCore(core, logger)
		return nil, fmt.Errorf("create telegram client: %w", err)
	}

	var assistantClient assistant.Client
	if cfg.BotToken != "" {
		app := assistant.NewApp(cfg.AppID, cfg.AppHash, cfg.BotToken, logger)
		if core.perms != nil {
			app.SetOwner(core.perms.OwnerID, core.perms.ListSudo)
		} else if cfg.OwnerID != 0 {
			app.SetOwner(cfg.OwnerID, nil)
		}

		assistantClient = app
	}

	return &telegramRuntime{
		client:     client,
		dispatcher: dispatcher,
		assistant:  assistantClient,
	}, nil
}
