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
}

func HelpView(model HelpModel) presentation.View {
	modules := HelpModules(model.Commands)
	card := ui.NewCard("Command Browser").
		WithIcon("📚").
		WithHeader("Browse the canonical Assistant command registry by module.").
		AddField("Commands", strconv.Itoa(len(model.Commands))).
		AddField("Modules", strconv.Itoa(len(modules)))

	rows := make([]presentation.Row, 0, 3)
	if len(modules) == 0 {
		card.WithRaw("<i>No Assistant commands are currently registered.</i>")
	} else {
		selected := clampIndex(model.Selected, len(modules))
		module := modules[selected]
		card.AddField("Selected", fmt.Sprintf("%s · %d commands", ui.EscapeHTML(module.Name), len(module.Commands))).
			AddField("Position", fmt.Sprintf("%d / %d", selected+1, len(modules))).
			WithFooter("<i>Open the selected module, or move through the bounded module navigator.</i>")
		if len(modules) > 1 {
			rows = append(rows, presentation.Row{
				{Text: "◀️ Previous", ActionID: ActionHelpPrev},
				{Text: "Open", ActionID: ActionHelpOpen},
				{Text: "Next ▶️", ActionID: ActionHelpNext},
			})
		} else {
			rows = append(rows, presentation.Row{{Text: "Open", ActionID: ActionHelpOpen}})
		}
	}
	rows = append(rows, presentation.Row{{Text: "🏠 Home", ActionID: ActionHome}})
	return presentation.View{Text: card.Render(), Rows: rows}
}

type HelpModuleModel struct {
	Module      HelpModule
	ModuleIndex int
	ModuleTotal int
	Selected    int
}

func HelpModuleView(model HelpModuleModel) presentation.View {
	commands := model.Module.Commands
	name := strings.TrimSpace(model.Module.Name)
	if name == "" {
		name = "General"
	}
	card := ui.NewCard(name).
		WithIcon("📂").
		WithHeader("Browse commands in this module.").
		AddField("Commands", strconv.Itoa(len(commands)))
	if model.ModuleTotal > 0 {
		card.AddField("Module", fmt.Sprintf("%d / %d", clampIndex(model.ModuleIndex, model.ModuleTotal)+1, model.ModuleTotal))
	}

	rows := make([]presentation.Row, 0, 3)
	if len(commands) == 0 {
		card.WithRaw("<i>No commands are currently available in this module.</i>")
	} else {
		selected := clampIndex(model.Selected, len(commands))
		command := commands[selected]
		label := "/" + command.Name
		if description := strings.TrimSpace(command.Description); description != "" {
			label += " — " + truncateHelp(description, 72)
		}
		card.AddField("Selected", ui.EscapeHTML(label)).
			AddField("Position", fmt.Sprintf("%d / %d", selected+1, len(commands))).
			WithFooter("<i>Open Details for usage, aliases, permissions, and execution constraints.</i>")
		if len(commands) > 1 {
			rows = append(rows, presentation.Row{
				{Text: "◀️ Previous", ActionID: ActionHelpCmdPrev},
				{Text: "Details", ActionID: ActionHelpCmdOpen},
				{Text: "Next ▶️", ActionID: ActionHelpCmdNext},
			})
		} else {
			rows = append(rows, presentation.Row{{Text: "Details", ActionID: ActionHelpCmdOpen}})
		}
	}
	rows = append(rows,
		presentation.Row{{Text: "📚 Modules", ActionID: ActionHelp}},
		presentation.Row{{Text: "🏠 Home", ActionID: ActionHome}},
	)
	return presentation.View{Text: card.Render(), Rows: rows}
}

type HelpCommandModel struct {
	Command core.Command
}

func HelpCommandView(model HelpCommandModel) presentation.View {
	command := model.Command
	name := strings.TrimSpace(command.Name)
	if name == "" {
		name = "unknown"
	}
	description := strings.TrimSpace(command.Description)
	if description == "" {
		description = "No command description is registered."
	}
	card := ui.NewCard("/" + name).
		WithIcon("📖").
		WithHeader(ui.EscapeHTML(description))
	if usage := strings.TrimSpace(command.Usage); usage != "" {
		card.AddField("Usage", ui.Code(usage))
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
			card.AddField("Aliases", ui.EscapeHTML(strings.Join(aliases, ", ")))
		}
	}
	category := strings.TrimSpace(command.Category)
	if category == "" {
		category = "General"
	}
	card.AddField("Module", ui.EscapeHTML(category)).
		AddField("Permission", command.Permission.String())
	contexts := make([]string, 0, 3)
	if command.GroupOnly {
		contexts = append(contexts, "Group only")
	}
	if command.PrivateOnly {
		contexts = append(contexts, "Private only")
	}
	if command.ReplyOnly {
		contexts = append(contexts, "Reply required")
	}
	if len(contexts) > 0 {
		card.AddField("Context", strings.Join(contexts, " · "))
	}
	if command.Cooldown > 0 {
		card.AddField("Cooldown", command.Cooldown.String())
	}
	if command.Timeout > 0 {
		card.AddField("Timeout", command.Timeout.String())
	}
	if len(command.Resources) > 0 {
		card.AddField("Resources", strconv.Itoa(len(command.Resources)))
	}
	card.WithFooter("<i>Execute it with /" + ui.EscapeHTML(name) + ".</i>")
	return presentation.View{
		Text: card.Render(),
		Rows: []presentation.Row{
			{{Text: "« Commands", ActionID: ActionHelpBack}, {Text: "📚 Modules", ActionID: ActionHelp}},
			{{Text: "🏠 Home", ActionID: ActionHome}},
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

func PublicStartView(username string) presentation.View {
	username = normalizedUsername(username)
	card := ui.NewCard("GoUltroid Assistant").
		WithIcon("🤖").
		WithHeader("Assistant endpoint is online.").
		AddField("Bot", "@"+username).
		WithRaw("<i>The interactive control shell is available only to its authorized owner.</i>")
	return presentation.View{Text: card.Render()}
}
