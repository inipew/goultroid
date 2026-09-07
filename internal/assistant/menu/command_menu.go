package menu

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

// RegisterTelegramCommandMenu synchronizes Telegram's native bot command menu
// from the canonical Assistant command surface. It never creates a second
// command registry: core.Router remains the source of truth.
func RegisterTelegramCommandMenu(ctx context.Context, api *tg.Client, router *core.Router) error {
	if api == nil {
		return fmt.Errorf("assistant/menu: telegram API is nil")
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

	// /start is the transport-level dashboard and therefore exists even when
	// the canonical plugin registry has not been attached yet.
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
		return fmt.Errorf("assistant/menu: register Telegram command menu: %w", err)
	}
	return nil
}
