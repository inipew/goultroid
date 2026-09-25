package shell

import (
	"fmt"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/presentation"
)

type inlineHelpSelection struct {
	View        presentation.View
	State       []byte
	Title       string
	Description string
}

func resolveInlineHelpSelection(commands []core.Command, target, locale string) (inlineHelpSelection, bool) {
	target = strings.TrimSpace(target)
	modules := HelpModules(commands)
	if target == "" {
		state := HelpState(InitialState(), commands, true)
		decoded := DecodeState(state)
		view := InlineHelpView(HelpView(HelpModel{Commands: commands, Page: int(decoded.CategoryIndex), Locale: locale}))
		return inlineHelpSelection{
			View:        view,
			State:       state,
			Title:       tr(locale, "assistant.help.title"),
			Description: tr(locale, "assistant.help.header"),
		}, true
	}

	for moduleIndex, module := range modules {
		for commandIndex, command := range module.Commands {
			if !helpCommandMatchesTarget(command, target) {
				continue
			}
			state := DecodeState(HelpState(InitialState(), commands, true))
			state.Screen = ScreenHelp
			state.CategoryIndex = uint16(moduleIndex)
			state.SettingIndex = uint16(commandIndex)
			setHelpBinding(&state, helpCommandDetailBinding(module, command))
			description := strings.TrimSpace(command.Description)
			if description == "" {
				description = tr(locale, "assistant.help.no_description")
			}
			return inlineHelpSelection{
				View:        InlineHelpView(HelpCommandView(HelpCommandModel{Command: command, Locale: locale})),
				State:       EncodeState(state),
				Title:       "/" + command.Name,
				Description: description,
			}, true
		}
	}

	for moduleIndex, module := range modules {
		if !strings.EqualFold(strings.TrimSpace(module.Name), target) {
			continue
		}
		state := DecodeState(HelpState(InitialState(), commands, true))
		state.Screen = ScreenHelp
		state.CategoryIndex = uint16(moduleIndex)
		state.SettingIndex = 0
		setHelpBinding(&state, helpCommandPageBinding(module, 0))
		return inlineHelpSelection{
			View: InlineHelpView(HelpModuleView(HelpModuleModel{
				Module:      module,
				ModuleIndex: moduleIndex,
				ModuleTotal: len(modules),
				Page:        0,
				Locale:      locale,
			})),
			State:       EncodeState(state),
			Title:       module.Name,
			Description: fmt.Sprintf("%d commands", len(module.Commands)),
		}, true
	}

	return inlineHelpSelection{}, false
}

func helpCommandMatchesTarget(command core.Command, target string) bool {
	if strings.EqualFold(strings.TrimSpace(command.Name), target) {
		return true
	}
	for _, alias := range command.Aliases {
		if strings.EqualFold(strings.TrimSpace(alias), target) {
			return true
		}
	}
	return false
}

// InlineHelpView removes message-only shell chrome from a canonical help view.
// Help navigation buttons remain unchanged and are compiled through the shared
// a2 interaction runtime for inline-message callbacks.
func InlineHelpView(view presentation.View) presentation.View {
	rows := make([]presentation.Row, 0, len(view.Rows))
	for _, row := range view.Rows {
		filtered := make(presentation.Row, 0, len(row))
		for _, button := range row {
			switch button.ActionID {
			case ActionHome, ActionClose:
				continue
			default:
				filtered = append(filtered, button)
			}
		}
		if len(filtered) > 0 {
			rows = append(rows, filtered)
		}
	}
	view.Rows = rows
	return view
}
