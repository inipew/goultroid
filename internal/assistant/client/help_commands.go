package client

import (
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
)

func (c *AssistantClient) shellHelpCommands(ctx *orchestration.Context) []core.Command {
	source := execution.SourceAssistant
	if isInlineInteractionTarget(ctx) {
		source = execution.SourceUserbot
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

func (c *AssistantClient) shellHelpPresentation(ctx *orchestration.Context, view presentation.View) presentation.View {
	if isInlineInteractionTarget(ctx) {
		return assistantshell.InlineHelpView(view)
	}
	return view
}
