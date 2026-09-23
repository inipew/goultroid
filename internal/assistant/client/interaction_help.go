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
	commands := c.shellCommands()
	modules := shell.HelpModules(commands)
	state := shell.StepHelpModuleState(ctx.State(), len(modules), delta)
	selected := int(shell.DecodeState(state).CategoryIndex)
	return ctx.Transition(state, 0, shell.HelpView(shell.HelpModel{Commands: commands, Selected: selected, Locale: c.shellInteractionLocale(ctx)}))
}

func (c *AssistantClient) handleShellHelpPrev(ctx *orchestration.Context) error {
	return c.stepShellHelpModule(ctx, -1)
}

func (c *AssistantClient) handleShellHelpNext(ctx *orchestration.Context) error {
	return c.stepShellHelpModule(ctx, 1)
}

func (c *AssistantClient) handleShellHelpOpen(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, shell.InteractionHelpModule); err != nil {
		return err
	}
	commands := c.shellCommands()
	modules := shell.HelpModules(commands)
	if len(modules) == 0 {
		return c.handleShellHelp(ctx)
	}
	state := shell.OpenHelpModuleState(ctx.State(), len(modules))
	decoded := shell.DecodeState(state)
	moduleIndex := selectionIndex(int(decoded.CategoryIndex), len(modules))
	return ctx.Transition(state, 0, shell.HelpModuleView(shell.HelpModuleModel{
		Module:      modules[moduleIndex],
		ModuleIndex: moduleIndex,
		ModuleTotal: len(modules),
		Selected:    int(decoded.SettingIndex),
		Locale:      c.shellInteractionLocale(ctx),
	}))
}

func (c *AssistantClient) stepShellHelpCommand(ctx *orchestration.Context, delta int) error {
	if err := c.admitShellScreen(ctx, shell.InteractionHelpModule); err != nil {
		return err
	}
	modules := shell.HelpModules(c.shellCommands())
	if len(modules) == 0 {
		return c.handleShellHelp(ctx)
	}
	decoded := shell.DecodeState(ctx.State())
	moduleIndex := selectionIndex(int(decoded.CategoryIndex), len(modules))
	module := modules[moduleIndex]
	state := shell.StepHelpCommandState(ctx.State(), len(module.Commands), delta)
	decoded = shell.DecodeState(state)
	return ctx.Transition(state, 0, shell.HelpModuleView(shell.HelpModuleModel{
		Module:      module,
		ModuleIndex: moduleIndex,
		ModuleTotal: len(modules),
		Selected:    int(decoded.SettingIndex),
		Locale:      c.shellInteractionLocale(ctx),
	}))
}

func (c *AssistantClient) handleShellHelpCmdPrev(ctx *orchestration.Context) error {
	return c.stepShellHelpCommand(ctx, -1)
}

func (c *AssistantClient) handleShellHelpCmdNext(ctx *orchestration.Context) error {
	return c.stepShellHelpCommand(ctx, 1)
}

func (c *AssistantClient) handleShellHelpCmdOpen(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, shell.InteractionHelpCommand); err != nil {
		return err
	}
	modules := shell.HelpModules(c.shellCommands())
	if len(modules) == 0 {
		return c.handleShellHelp(ctx)
	}
	decoded := shell.DecodeState(ctx.State())
	moduleIndex := selectionIndex(int(decoded.CategoryIndex), len(modules))
	module := modules[moduleIndex]
	if len(module.Commands) == 0 {
		return c.handleShellHelpOpen(ctx)
	}
	state := shell.OpenHelpCommandState(ctx.State(), len(module.Commands))
	decoded = shell.DecodeState(state)
	commandIndex := selectionIndex(int(decoded.SettingIndex), len(module.Commands))
	return ctx.Transition(state, 0, shell.HelpCommandView(shell.HelpCommandModel{Command: module.Commands[commandIndex], Locale: c.shellInteractionLocale(ctx)}))
}

func (c *AssistantClient) handleShellHelpBack(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, shell.InteractionHelpModule); err != nil {
		return err
	}
	modules := shell.HelpModules(c.shellCommands())
	if len(modules) == 0 {
		return c.handleShellHelp(ctx)
	}
	decoded := shell.DecodeState(ctx.State())
	moduleIndex := selectionIndex(int(decoded.CategoryIndex), len(modules))
	module := modules[moduleIndex]
	state := shell.BackHelpModuleState(ctx.State(), len(modules), len(module.Commands))
	decoded = shell.DecodeState(state)
	return ctx.Transition(state, 0, shell.HelpModuleView(shell.HelpModuleModel{
		Module:      module,
		ModuleIndex: moduleIndex,
		ModuleTotal: len(modules),
		Selected:    int(decoded.SettingIndex),
		Locale:      c.shellInteractionLocale(ctx),
	}))
}
