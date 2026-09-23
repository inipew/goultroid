package shell

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/ui"
)

const (
	InteractionHelpModule  = "help_module"
	InteractionHelpCommand = "help_command"

	ActionHelpPrev    = "help_prev"
	ActionHelpNext    = "help_next"
	ActionHelpOpen    = "help_open"
	ActionHelpCmdPrev = "help_cmd_prev"
	ActionHelpCmdNext = "help_cmd_next"
	ActionHelpCmdOpen = "help_cmd_open"
	ActionHelpBack    = "help_back"
	ActionClose       = "close"
)

type HelpModule struct {
	Name     string
	Commands []core.Command
}

func HelpModules(commands []core.Command) []HelpModule {
	grouped := make(map[string][]core.Command)
	for _, command := range commands {
		category := strings.TrimSpace(command.Category)
		if category == "" {
			category = "General"
		}
		grouped[category] = append(grouped[category], command)
	}
	modules := make([]HelpModule, 0, len(grouped))
	for category, moduleCommands := range grouped {
		sort.SliceStable(moduleCommands, func(i, j int) bool {
			left := strings.ToLower(strings.TrimSpace(moduleCommands[i].Name))
			right := strings.ToLower(strings.TrimSpace(moduleCommands[j].Name))
			if left == right {
				return moduleCommands[i].Name < moduleCommands[j].Name
			}
			return left < right
		})
		modules = append(modules, HelpModule{Name: category, Commands: moduleCommands})
	}
	sort.SliceStable(modules, func(i, j int) bool {
		left := strings.ToLower(modules[i].Name)
		right := strings.ToLower(modules[j].Name)
		if left == right {
			return modules[i].Name < modules[j].Name
		}
		return left < right
	})
	return modules
}

func HelpState(raw []byte, reset bool) []byte {
	state := DecodeState(raw)
	wasHelp := state.Screen == ScreenHelp
	state.Screen = ScreenHelp
	clearSettingBinding(&state)
	if reset || !wasHelp {
		state.CategoryIndex = 0
		state.SettingIndex = 0
	}
	return EncodeState(state)
}

func StepHelpModuleState(raw []byte, total, delta int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenHelp
	state.CategoryIndex = uint16(stepIndex(int(state.CategoryIndex), total, delta))
	state.SettingIndex = 0
	clearSettingBinding(&state)
	return EncodeState(state)
}

func OpenHelpModuleState(raw []byte, total int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenHelp
	state.CategoryIndex = uint16(clampIndex(int(state.CategoryIndex), total))
	state.SettingIndex = 0
	clearSettingBinding(&state)
	return EncodeState(state)
}

func StepHelpCommandState(raw []byte, total, delta int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenHelp
	state.SettingIndex = uint16(stepIndex(int(state.SettingIndex), total, delta))
	clearSettingBinding(&state)
	return EncodeState(state)
}

func OpenHelpCommandState(raw []byte, total int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenHelp
	state.SettingIndex = uint16(clampIndex(int(state.SettingIndex), total))
	clearSettingBinding(&state)
	return EncodeState(state)
}

func BackHelpModuleState(raw []byte, moduleTotal, commandTotal int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenHelp
	state.CategoryIndex = uint16(clampIndex(int(state.CategoryIndex), moduleTotal))
	state.SettingIndex = uint16(clampIndex(int(state.SettingIndex), commandTotal))
	clearSettingBinding(&state)
	return EncodeState(state)
}

type HelpModel struct {
	Commands []core.Command
	Selected int
	Locale   string
}

func HelpView(model HelpModel) presentation.View {
	locale := shellLocale(model.Locale)
	modules := HelpModules(model.Commands)
	card := ui.NewCard(tr(locale, "assistant.help.title")).
		WithIcon("📚").
		WithHeader(tr(locale, "assistant.help.header")).
		AddField(tr(locale, "assistant.help.commands"), strconv.Itoa(len(model.Commands))).
		AddField(tr(locale, "assistant.help.modules"), strconv.Itoa(len(modules)))

	rows := make([]presentation.Row, 0, 3)
	if len(modules) == 0 {
		card.WithRaw(tr(locale, "assistant.help.empty"))
	} else {
		selected := clampIndex(model.Selected, len(modules))
		module := modules[selected]
		card.AddField(tr(locale, "assistant.help.selected"), fmt.Sprintf("%s · %d", ui.EscapeHTML(module.Name), len(module.Commands))).
			AddField(tr(locale, "assistant.help.position"), fmt.Sprintf("%d / %d", selected+1, len(modules))).
			WithFooter(tr(locale, "assistant.help.footer"))
		if len(modules) > 1 {
			rows = append(rows, presentation.Row{
				{Text: tr(locale, "assistant.button.previous"), ActionID: ActionHelpPrev},
				{Text: tr(locale, "assistant.button.open"), ActionID: ActionHelpOpen},
				{Text: tr(locale, "assistant.button.next"), ActionID: ActionHelpNext},
			})
		} else {
			rows = append(rows, presentation.Row{{Text: tr(locale, "assistant.button.open"), ActionID: ActionHelpOpen}})
		}
	}
	rows = append(rows, presentation.Row{{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome}})
	return presentation.View{Text: card.Render(), Rows: rows}
}

type HelpModuleModel struct {
	Module      HelpModule
	ModuleIndex int
	ModuleTotal int
	Selected    int
	Locale      string
}

func HelpModuleView(model HelpModuleModel) presentation.View {
	locale := shellLocale(model.Locale)
	commands := model.Module.Commands
	name := strings.TrimSpace(model.Module.Name)
	if name == "" {
		name = "General"
	}
	card := ui.NewCard(name).
		WithIcon("📂").
		WithHeader(tr(locale, "assistant.help.module_header")).
		AddField(tr(locale, "assistant.help.commands"), strconv.Itoa(len(commands)))
	if model.ModuleTotal > 0 {
		card.AddField(tr(locale, "assistant.help.module"), fmt.Sprintf("%d / %d", clampIndex(model.ModuleIndex, model.ModuleTotal)+1, model.ModuleTotal))
	}

	rows := make([]presentation.Row, 0, 3)
	if len(commands) == 0 {
		card.WithRaw(tr(locale, "assistant.help.module_empty"))
	} else {
		selected := clampIndex(model.Selected, len(commands))
		command := commands[selected]
		label := "/" + command.Name
		if description := strings.TrimSpace(command.Description); description != "" {
			label += " — " + truncateHelp(description, 72)
		}
		card.AddField(tr(locale, "assistant.help.selected"), ui.EscapeHTML(label)).
			AddField(tr(locale, "assistant.help.position"), fmt.Sprintf("%d / %d", selected+1, len(commands))).
			WithFooter(tr(locale, "assistant.help.module_footer"))
		if len(commands) > 1 {
			rows = append(rows, presentation.Row{
				{Text: tr(locale, "assistant.button.previous"), ActionID: ActionHelpCmdPrev},
				{Text: tr(locale, "assistant.button.details"), ActionID: ActionHelpCmdOpen},
				{Text: tr(locale, "assistant.button.next"), ActionID: ActionHelpCmdNext},
			})
		} else {
			rows = append(rows, presentation.Row{{Text: tr(locale, "assistant.button.details"), ActionID: ActionHelpCmdOpen}})
		}
	}
	rows = append(rows,
		presentation.Row{{Text: tr(locale, "assistant.button.modules"), ActionID: ActionHelp}},
		presentation.Row{{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome}},
	)
	return presentation.View{Text: card.Render(), Rows: rows}
}

type HelpCommandModel struct {
	Command core.Command
	Locale  string
}

func HelpCommandView(model HelpCommandModel) presentation.View {
	locale := shellLocale(model.Locale)
	command := model.Command
	name := strings.TrimSpace(command.Name)
	if name == "" {
		name = "unknown"
	}
	description := strings.TrimSpace(command.Description)
	if description == "" {
		description = tr(locale, "assistant.help.no_description")
	}
	card := ui.NewCard("/" + name).
		WithIcon("📖").
		WithHeader(ui.EscapeHTML(description))
	if usage := strings.TrimSpace(command.Usage); usage != "" {
		card.AddField(tr(locale, "assistant.help.usage"), ui.Code(usage))
	}
	if len(command.Aliases) > 0 {
		aliases := make([]string, 0, len(command.Aliases))
		for _, alias := range command.Aliases {
			alias = strings.TrimSpace(alias)
			if alias != "" {
				aliases = append(aliases, "/"+alias)
			}
		}
		if len(aliases) > 0 {
			card.AddField(tr(locale, "assistant.help.aliases"), ui.EscapeHTML(strings.Join(aliases, ", ")))
		}
	}
	category := strings.TrimSpace(command.Category)
	if category == "" {
		category = "General"
	}
	card.AddField(tr(locale, "assistant.help.module"), ui.EscapeHTML(category)).
		AddField(tr(locale, "assistant.help.permission"), command.Permission.String())
	contexts := make([]string, 0, 3)
	if command.GroupOnly {
		contexts = append(contexts, tr(locale, "assistant.help.group_only"))
	}
	if command.PrivateOnly {
		contexts = append(contexts, tr(locale, "assistant.help.private_only"))
	}
	if command.ReplyOnly {
		contexts = append(contexts, tr(locale, "assistant.help.reply_required"))
	}
	if len(contexts) > 0 {
		card.AddField(tr(locale, "assistant.help.context"), strings.Join(contexts, " · "))
	}
	if command.Cooldown > 0 {
		card.AddField(tr(locale, "assistant.help.cooldown"), command.Cooldown.String())
	}
	if command.Timeout > 0 {
		card.AddField(tr(locale, "assistant.help.timeout"), command.Timeout.String())
	}
	if len(command.Resources) > 0 {
		card.AddField(tr(locale, "assistant.help.resources"), strconv.Itoa(len(command.Resources)))
	}
	card.WithFooter(tr(locale, "assistant.help.execute", ui.EscapeHTML(name)))
	return presentation.View{
		Text: card.Render(),
		Rows: []presentation.Row{
			{{Text: tr(locale, "assistant.button.commands"), ActionID: ActionHelpBack}, {Text: tr(locale, "assistant.button.modules"), ActionID: ActionHelp}},
			{{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome}},
		},
	}
}

func truncateHelp(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

type PublicStartModel struct {
	Username       string
	Locale         string
	RelayAvailable bool
}

func PublicStartView(model PublicStartModel) presentation.View {
	locale := shellLocale(model.Locale)
	username := normalizedUsername(model.Username)
	text := fmt.Sprintf(
		tr(locale, "assistant.public.header"),
		ui.EscapeHTML("@"+username),
	)
	if model.RelayAvailable {
		text += "\n\n" + tr(locale, "assistant.public.relay_hint")
	}
	return presentation.View{Text: text}
}
