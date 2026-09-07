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
		bot := assistant.NewBotClient(cfg.AppID, cfg.AppHash, cfg.BotToken, logger)
		bot.SetCallbackRouter(core.callbackRouter)
		bot.SetInlineEngine(core.inlineEngine)
		bot.SetLocalizer(core.localizer)
		bot.SetEventBus(core.eventBus)

		// Register assistant callback handler so /start and status buttons are routed properly
		assistantHandler := assistant.NewHandler(bot, bot.StartTime())
		if err := core.callbackRouter.Register(assistantHandler); err != nil {
			logger.Warn("failed to register assistant callback handler", zap.Error(err))
		}

		assistantClient = bot
	}

	return &telegramRuntime{
		client:     client,
		dispatcher: dispatcher,
		assistant:  assistantClient,
	}, nil
}
