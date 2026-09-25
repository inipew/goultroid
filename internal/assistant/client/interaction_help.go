package client

import (
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
)

func (c *AssistantClient) dispatchPublicStart(ctx *command.Context) error {
	if ctx == nil || ctx.Peer == nil || ctx.Interaction == nil {
		return ErrShellUnavailable
	}
	chatID := extractChatIDFromInputPeer(ctx.Peer)
	if chatID == 0 {
		chatID = ctx.SenderID
	}
	view := shell.PublicStartView(shell.PublicStartModel{
		Username:       c.Username(),
		Locale:         c.shellLocale(ctx.Ctx, ctx.SenderID, chatID),
		RelayAvailable: c.publicStartRelayAvailable(),
	})
	_, err := ctx.Reply(view.Text, nil)
	return err
}

func (c *AssistantClient) publicStartRelayAvailable() bool {
	c.mu.RLock()
	relay := c.pmRelay
	c.mu.RUnlock()
	if relay == nil {
		return false
	}
	if state, ok := relay.(interface{ IsEnabled() bool }); ok {
		return state.IsEnabled()
	}
	return true
}

func (c *AssistantClient) stepShellHelpModule(ctx *orchestration.Context, delta int) error {
	if err := c.admitShellScreen(ctx, shell.InteractionHelp); err != nil {
		return err
	}
	commands := c.shellHelpCommands(ctx)
	state, ok := shell.StepHelpModuleState(ctx.State(), commands, delta)
	if !ok {
		return ErrShellHelpSelectionStale
	}
	page := int(shell.DecodeState(state).CategoryIndex)
	view := shell.HelpView(shell.HelpModel{Commands: commands, Page: page, Locale: c.shellInteractionLocale(ctx)})
	return ctx.Transition(state, 0, c.shellHelpPresentation(ctx, view))
}

func (c *AssistantClient) handleShellHelpPrev(ctx *orchestration.Context) error {
	return c.stepShellHelpModule(ctx, -1)
}

func (c *AssistantClient) handleShellHelpNext(ctx *orchestration.Context) error {
	return c.stepShellHelpModule(ctx, 1)
}

func (c *AssistantClient) handleShellHelpModuleSlot(ctx *orchestration.Context, slot int) error {
	if err := c.admitShellScreen(ctx, shell.InteractionHelpModule); err != nil {
		return err
	}
	commands := c.shellHelpCommands(ctx)
	state, module, _, ok := shell.OpenHelpModuleSlotState(ctx.State(), commands, slot)
	if !ok {
		return ErrShellHelpSelectionStale
	}
	view := shell.HelpModuleView(shell.HelpModuleModel{
		Module: module,
		Page:   int(shell.DecodeState(state).SettingIndex),
		Locale: c.shellInteractionLocale(ctx),
	})
	return ctx.Transition(state, 0, c.shellHelpPresentation(ctx, view))
}

func (c *AssistantClient) stepShellHelpCommand(ctx *orchestration.Context, delta int) error {
	if err := c.admitShellScreen(ctx, shell.InteractionHelpModule); err != nil {
		return err
	}
	commands := c.shellHelpCommands(ctx)
	state, module, _, ok := shell.StepHelpCommandState(ctx.State(), commands, delta)
	if !ok {
		return ErrShellHelpSelectionStale
	}
	view := shell.HelpModuleView(shell.HelpModuleModel{
		Module: module,
		Page:   int(shell.DecodeState(state).SettingIndex),
		Locale: c.shellInteractionLocale(ctx),
	})
	return ctx.Transition(state, 0, c.shellHelpPresentation(ctx, view))
}

func (c *AssistantClient) handleShellHelpCmdPrev(ctx *orchestration.Context) error {
	return c.stepShellHelpCommand(ctx, -1)
}

func (c *AssistantClient) handleShellHelpCmdNext(ctx *orchestration.Context) error {
	return c.stepShellHelpCommand(ctx, 1)
}

func (c *AssistantClient) handleShellHelpCommandSlot(ctx *orchestration.Context, slot int) error {
	if err := c.admitShellScreen(ctx, shell.InteractionHelpCommand); err != nil {
		return err
	}
	state, command, _, _, ok := shell.OpenHelpCommandSlotState(ctx.State(), c.shellHelpCommands(ctx), slot)
	if !ok {
		return ErrShellHelpSelectionStale
	}
	view := shell.HelpCommandView(shell.HelpCommandModel{Command: command, Locale: c.shellInteractionLocale(ctx)})
	return ctx.Transition(state, 0, c.shellHelpPresentation(ctx, view))
}

func (c *AssistantClient) handleShellHelpBack(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, shell.InteractionHelpModule); err != nil {
		return err
	}
	commands := c.shellHelpCommands(ctx)
	state, module, _, ok := shell.BackHelpModuleState(ctx.State(), commands)
	if !ok {
		return ErrShellHelpSelectionStale
	}
	view := shell.HelpModuleView(shell.HelpModuleModel{
		Module: module,
		Page:   int(shell.DecodeState(state).SettingIndex),
		Locale: c.shellInteractionLocale(ctx),
	})
	return ctx.Transition(state, 0, c.shellHelpPresentation(ctx, view))
}
