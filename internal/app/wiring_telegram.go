package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/inipew/goultroid/internal/assistant"
	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/telegram"
	"go.uber.org/zap"
)

func readDCFromSession(path string) int {
	if envDC := os.Getenv("TELEGRAM_DC"); envDC != "" {
		if dc, err := strconv.Atoi(envDC); err == nil && dc > 0 {
			return dc
		}
	}
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var s struct {
		Data struct {
			DC int `json:"DC"`
		} `json:"Data"`
	}
	if err := json.Unmarshal(data, &s); err == nil && s.Data.DC > 0 {
		return s.Data.DC
	}
	return 0
}

func buildTelegramRuntime(cfg *config.Config, core *coreDependencies, logger *zap.Logger) (*telegramRuntime, error) {
	dispatcherDeps := telegram.DispatcherDeps{
		Router:       core.router,
		Permissions:  core.perms,
		Service:      nil,
		Logger:       logger,
		EventBus:     core.eventBus,
		Localizer:    core.localizer,
		InlineEngine: core.inlineEngine,
		Tasks:        core.taskEngine,
	}
	dispatcher, err := telegram.NewDispatcherWithDeps(dispatcherDeps)
	if err != nil {
		return nil, fmt.Errorf("create dispatcher: %w", err)
	}
	dispatcher.Executor().SetMetrics(core.metrics)
	dispatcher.Executor().SetRateLimiter(commandRateLimiterAdapter{limiter: core.cmdLimiter})
	if core.idempManager != nil {
		dispatcher.SetIdempotency(core.idempManager)
	}

	client, err := telegram.NewClient(cfg, dispatcher, core.db, logger)
	if err != nil {
		return nil, fmt.Errorf("create telegram client: %w", err)
	}

	var assistantClient assistant.Client
	if cfg.BotToken != "" {
		app := assistant.NewApp(cfg.AppID, cfg.AppHash, cfg.BotToken, logger)
		if dc := readDCFromSession(cfg.SessionFile); dc > 0 {
			logger.Info("assistant: configuring datacenter", zap.Int("dc", dc))
			app.SetDC(dc)
		}
		app.SetRPCExecutor(assistantRPCExecutor{executor: client.Executor()})
		app.SetTasks(core.taskEngine)
		app.SetCommandExecutor(dispatcher.Executor())
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
