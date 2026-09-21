package command

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

const (
	maxBotCommands           = 100
	maxBotCommandDescription = 256
)

type botCommandAPI interface {
	BotsSetBotCommands(context.Context, *tg.BotsSetBotCommandsRequest) (bool, error)
}

// RegisterTelegramCommandMenu synchronizes Telegram's native bot command list
// from the canonical Assistant command surface. core.Router remains the only
// command registry; this is transport presentation only.
func RegisterTelegramCommandMenu(ctx context.Context, api botCommandAPI, router *core.Router) error {
	if api == nil {
		return fmt.Errorf("assistant/command: telegram API is nil")
	}

	commands := make([]tg.BotCommand, 0, maxBotCommands)
	seen := make(map[string]struct{}, maxBotCommands)
	appendCommand := func(name, description string) {
		name = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "/")
		if name == "" || len(commands) >= maxBotCommands {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		if description == "" {
			description = "GoUltroid Assistant command"
		}
		if len(description) > maxBotCommandDescription {
			description = description[:maxBotCommandDescription]
		}
		commands = append(commands, tg.BotCommand{Command: name, Description: description})
	}

	appendCommand("start", "Open the GoUltroid Assistant dashboard")
	if router != nil {
		for _, cmd := range router.CommandsForSurface(execution.SourceAssistant) {
			appendCommand(cmd.Name, cmd.Description)
		}
	}

	if _, err := api.BotsSetBotCommands(ctx, &tg.BotsSetBotCommandsRequest{
		Scope:    &tg.BotCommandScopeDefault{},
		LangCode: "",
		Commands: commands,
	}); err != nil {
		return fmt.Errorf("assistant/command: register Telegram command menu: %w", err)
	}
	return nil
}
