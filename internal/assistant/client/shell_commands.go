package client

import (
	"strings"

	"github.com/inipew/goultroid/internal/assistant/command"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
)

func (c *AssistantClient) dispatchShellHelpCommand(cmdCtx *command.Context) error {
	if cmdCtx == nil || cmdCtx.Peer == nil || cmdCtx.SenderID == 0 {
		return ErrShellUnavailable
	}
	commands := c.assistantHelpCommands()
	chatID := extractChatIDFromInputPeer(cmdCtx.Peer)
	if chatID == 0 {
		chatID = cmdCtx.SenderID
	}
	locale := c.shellLocale(cmdCtx.Ctx, cmdCtx.SenderID, chatID)
	target := ""
	if len(cmdCtx.Args) > 0 {
		target = strings.TrimSpace(strings.TrimLeft(cmdCtx.Args[0], "./"))
	}
	state, view, ok := resolveAssistantHelpSelection(commands, target, locale)
	if !ok {
		_, err := cmdCtx.Reply("⚠️ Command or module not found.", nil)
		return err
	}
	return c.beginShellCommand(cmdCtx, assistantshell.InteractionHelp, state, view)
}

func (c *AssistantClient) dispatchShellSettingsCommand(cmdCtx *command.Context) error {
	if cmdCtx == nil || cmdCtx.Peer == nil || cmdCtx.SenderID == 0 {
		return ErrShellUnavailable
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	chatID := extractChatIDFromInputPeer(cmdCtx.Peer)
	if chatID == 0 {
		chatID = cmdCtx.SenderID
	}
	locale := c.shellLocale(cmdCtx.Ctx, cmdCtx.SenderID, chatID)
	categories := shellSettingsCategories(svc.Registry(), locale)
	state := assistantshell.SettingsHomeState(assistantshell.InitialState(), categories, true)
	view := assistantshell.SettingsHomeView(assistantshell.SettingsHomeModel{
		Categories: categories,
		Page:       0,
		Locale:     locale,
	})

	if len(cmdCtx.Args) > 0 {
		target := strings.TrimSpace(cmdCtx.Args[0])
		for index, category := range categories {
			if !strings.EqualFold(category.ID, target) && !strings.EqualFold(category.Label, target) {
				continue
			}
			page := index / assistantshell.SettingsCategorySlotCount
			for current := 0; current < page; current++ {
				next, ok := assistantshell.StepSettingsCategoryPageState(state, categories, 1)
				if !ok {
					break
				}
				state = next
			}
			selected, selectedIndex, ok := assistantshell.ResolveSettingsCategorySlot(
				state,
				categories,
				index%assistantshell.SettingsCategorySlotCount,
			)
			if ok {
				defs := svc.Registry().ListByCategory(selected.ID)
				selected.Count = len(defs)
				state = assistantshell.SettingsCategoryState(state, selectedIndex, selected, defs, 0)
				view = assistantshell.SettingsCategoryView(assistantshell.SettingsCategoryModel{
					Category:    selected,
					Definitions: defs,
					Page:        0,
					Locale:      locale,
				})
			}
			break
		}
	}

	return c.beginShellCommand(cmdCtx, assistantshell.InteractionSettings, state, view)
}

func (c *AssistantClient) beginShellCommand(
	cmdCtx *command.Context,
	interactionID string,
	state []byte,
	view presentation.View,
) error {
	if cmdCtx == nil || cmdCtx.Peer == nil || cmdCtx.SenderID == 0 {
		return ErrShellUnavailable
	}
	c.mu.RLock()
	ingress := c.interactionIngress
	catalog := c.featureCatalog
	c.mu.RUnlock()
	if ingress == nil || ingress.engine == nil || catalog == nil {
		return ErrShellUnavailable
	}
	if err := c.admitShellInteraction(
		catalog,
		feature.InteractionScreen,
		interactionID,
		cmdCtx.SenderID,
		isPrivatePeer(cmdCtx.Peer),
	); err != nil {
		return err
	}
	if err := c.ensureShellActions(ingress.engine, catalog); err != nil {
		return err
	}
	chatID := extractChatIDFromInputPeer(cmdCtx.Peer)
	if chatID == 0 {
		chatID = cmdCtx.SenderID
	}
	_, err := ingress.engine.Begin(cmdCtx.Ctx, orchestration.BeginRequest{
		FeatureID: assistantshell.FeatureID,
		ActorID:   cmdCtx.SenderID,
		State:     state,
		TTL:       assistantshell.InteractionTTL,
		Target: presentationtelegram.MessageTarget{
			Peer:   cmdCtx.Peer,
			ChatID: chatID,
		},
		View: view,
	})
	return err
}

func (c *AssistantClient) assistantHelpCommands() []core.Command {
	if c == nil || c.cmdRouter == nil || c.cmdRouter.CoreRouter() == nil {
		return nil
	}
	return c.cmdRouter.CoreRouter().CommandsForSurface(execution.SourceAssistant)
}

func resolveAssistantHelpSelection(
	commands []core.Command,
	target string,
	locale string,
) ([]byte, presentation.View, bool) {
	state := assistantshell.HelpState(assistantshell.InitialState(), commands, true)
	decoded := assistantshell.DecodeState(state)
	if strings.TrimSpace(target) == "" {
		return state, assistantshell.HelpView(assistantshell.HelpModel{
			Commands: commands,
			Page:     int(decoded.CategoryIndex),
			Locale:   locale,
		}), true
	}

	modules := assistantshell.HelpModules(commands)
	for moduleIndex, module := range modules {
		for commandIndex, item := range module.Commands {
			if !assistantHelpCommandMatches(item, target) {
				continue
			}
			moduleState, _, _, ok := openAssistantHelpModule(state, commands, moduleIndex)
			if !ok {
				return nil, presentation.View{}, false
			}
			for page := 0; page < commandIndex/assistantshell.HelpCommandSlotCount; page++ {
				next, _, _, stepOK := assistantshell.StepHelpCommandState(moduleState, commands, 1)
				if !stepOK {
					return nil, presentation.View{}, false
				}
				moduleState = next
			}
			detailState, selectedCommand, _, _, ok := assistantshell.OpenHelpCommandSlotState(
				moduleState,
				commands,
				commandIndex%assistantshell.HelpCommandSlotCount,
			)
			if !ok {
				return nil, presentation.View{}, false
			}
			return detailState, assistantshell.HelpCommandView(assistantshell.HelpCommandModel{
				Command: selectedCommand,
				Locale:  locale,
			}), true
		}
	}

	for moduleIndex, module := range modules {
		if !strings.EqualFold(strings.TrimSpace(module.Name), strings.TrimSpace(target)) {
			continue
		}
		moduleState, selectedModule, selectedIndex, ok := openAssistantHelpModule(state, commands, moduleIndex)
		if !ok {
			return nil, presentation.View{}, false
		}
		return moduleState, assistantshell.HelpModuleView(assistantshell.HelpModuleModel{
			Module:      selectedModule,
			ModuleIndex: selectedIndex,
			ModuleTotal: len(modules),
			Page:        0,
			Locale:      locale,
		}), true
	}

	return nil, presentation.View{}, false
}

func openAssistantHelpModule(
	state []byte,
	commands []core.Command,
	moduleIndex int,
) ([]byte, assistantshell.HelpModule, int, bool) {
	for page := 0; page < moduleIndex/assistantshell.HelpModuleSlotCount; page++ {
		next, ok := assistantshell.StepHelpModuleState(state, commands, 1)
		if !ok {
			return nil, assistantshell.HelpModule{}, 0, false
		}
		state = next
	}
	return assistantshell.OpenHelpModuleSlotState(
		state,
		commands,
		moduleIndex%assistantshell.HelpModuleSlotCount,
	)
}

func assistantHelpCommandMatches(item core.Command, target string) bool {
	if strings.EqualFold(strings.TrimSpace(item.Name), strings.TrimSpace(target)) {
		return true
	}
	for _, alias := range item.Aliases {
		if strings.EqualFold(strings.TrimSpace(alias), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
