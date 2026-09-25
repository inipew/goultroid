package client

import (
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
)

func (c *AssistantClient) shellHelpCommands(ctx *orchestration.Context) []core.Command {
	source := execution.SourceAssistant
	if ctx != nil {
		if _, ok := ctx.Target().(presentationtelegram.InlineTarget); ok {
			source = execution.SourceUserbot
		}
	}
	if c == nil || c.cmdRouter == nil {
		return nil
	}
	router := c.cmdRouter.CoreRouter()
	if router == nil {
		return nil
	}
	return router.CommandsForSurface(source)
}
