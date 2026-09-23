package shell

import (
	"crypto/sha256"
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
	ActionHelpCmdPrev = "help_cmd_prev"
	ActionHelpCmdNext = "help_cmd_next"
	ActionHelpBack    = "help_back"
	ActionClose       = "close"

	HelpModuleSlotCount  = 8
	HelpCommandSlotCount = 8
)

var helpModuleSlotActions = [HelpModuleSlotCount]string{
	"help_module_slot_0", "help_module_slot_1", "help_module_slot_2", "help_module_slot_3",
	"help_module_slot_4", "help_module_slot_5", "help_module_slot_6", "help_module_slot_7",
}

var helpCommandSlotActions = [HelpCommandSlotCount]string{
	"help_command_slot_0", "help_command_slot_1", "help_command_slot_2", "help_command_slot_3",
	"help_command_slot_4", "help_command_slot_5", "help_command_slot_6", "help_command_slot_7",
}

func HelpModuleSlotActionIDs() []string  { return append([]string(nil), helpModuleSlotActions[:]...) }
func HelpCommandSlotActionIDs() []string { return append([]string(nil), helpCommandSlotActions[:]...) }

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

func HelpState(raw []byte, commands []core.Command, reset bool) []byte {
	modules := HelpModules(commands)
	state := DecodeState(raw)
	page := 0
	if !reset && state.Screen == ScreenHelp {
		if current, ok := helpRootPageFromState(state, modules); ok {
			page = current
		}
	}
	_, _, page, _ = helpPageWindow(len(modules), page, HelpModuleSlotCount)
	state.Screen = ScreenHelp
	state.CategoryIndex = uint16(page)
	state.SettingIndex = 0
	setHelpBinding(&state, helpModulePageBinding(modules, page))
	return EncodeState(state)
}

func StepHelpModuleState(raw []byte, commands []core.Command, delta int) ([]byte, bool) {
	modules := HelpModules(commands)
	state := DecodeState(raw)
	page := int(state.CategoryIndex)
	if state.Screen != ScreenHelp || !helpBindingMatches(state, helpModulePageBinding(modules, page)) {
		return nil, false
	}
	pages := helpPageCount(len(modules), HelpModuleSlotCount)
	page = stepIndex(page, pages, delta)
	state.CategoryIndex = uint16(page)
	state.SettingIndex = 0
	setHelpBinding(&state, helpModulePageBinding(modules, page))
	return EncodeState(state), true
}

func OpenHelpModuleSlotState(raw []byte, commands []core.Command, slot int) ([]byte, HelpModule, int, bool) {
	modules := HelpModules(commands)
	state := DecodeState(raw)
	page := int(state.CategoryIndex)
	if slot < 0 || slot >= HelpModuleSlotCount || state.Screen != ScreenHelp || !helpBindingMatches(state, helpModulePageBinding(modules, page)) {
		return nil, HelpModule{}, 0, false
	}
	start, end, page, _ := helpPageWindow(len(modules), page, HelpModuleSlotCount)
	index := start + slot
	if index < start || index >= end {
		return nil, HelpModule{}, 0, false
	}
	module := modules[index]
	state.CategoryIndex = uint16(index)
	state.SettingIndex = 0
	setHelpBinding(&state, helpCommandPageBinding(module, 0))
	return EncodeState(state), module, index, true
}

func StepHelpCommandState(raw []byte, commands []core.Command, delta int) ([]byte, HelpModule, int, bool) {
	modules := HelpModules(commands)
	state := DecodeState(raw)
	moduleIndex := int(state.CategoryIndex)
	if state.Screen != ScreenHelp || moduleIndex < 0 || moduleIndex >= len(modules) {
		return nil, HelpModule{}, 0, false
	}
	module := modules[moduleIndex]
	page := int(state.SettingIndex)
	if !helpBindingMatches(state, helpCommandPageBinding(module, page)) {
		return nil, HelpModule{}, 0, false
	}
	pages := helpPageCount(len(module.Commands), HelpCommandSlotCount)
	page = stepIndex(page, pages, delta)
	state.SettingIndex = uint16(page)
	setHelpBinding(&state, helpCommandPageBinding(module, page))
	return EncodeState(state), module, moduleIndex, true
}

func OpenHelpCommandSlotState(raw []byte, commands []core.Command, slot int) ([]byte, core.Command, int, int, bool) {
	modules := HelpModules(commands)
	state := DecodeState(raw)
	moduleIndex := int(state.CategoryIndex)
	if slot < 0 || slot >= HelpCommandSlotCount || state.Screen != ScreenHelp || moduleIndex < 0 || moduleIndex >= len(modules) {
		return nil, core.Command{}, 0, 0, false
	}
	module := modules[moduleIndex]
	page := int(state.SettingIndex)
	if !helpBindingMatches(state, helpCommandPageBinding(module, page)) {
		return nil, core.Command{}, 0, 0, false
	}
	start, end, _, _ := helpPageWindow(len(module.Commands), page, HelpCommandSlotCount)
	commandIndex := start + slot
	if commandIndex < start || commandIndex >= end {
		return nil, core.Command{}, 0, 0, false
	}
	command := module.Commands[commandIndex]
	state.SettingIndex = uint16(commandIndex)
	setHelpBinding(&state, helpCommandDetailBinding(module, command))
	return EncodeState(state), command, moduleIndex, commandIndex, true
}

func BackHelpModuleState(raw []byte, commands []core.Command) ([]byte, HelpModule, int, bool) {
	modules := HelpModules(commands)
	state := DecodeState(raw)
	moduleIndex := int(state.CategoryIndex)
	commandIndex := int(state.SettingIndex)
	if state.Screen != ScreenHelp || moduleIndex < 0 || moduleIndex >= len(modules) {
		return nil, HelpModule{}, 0, false
	}
	module := modules[moduleIndex]
	if commandIndex < 0 || commandIndex >= len(module.Commands) || !helpBindingMatches(state, helpCommandDetailBinding(module, module.Commands[commandIndex])) {
		return nil, HelpModule{}, 0, false
	}
	page := commandIndex / HelpCommandSlotCount
	state.SettingIndex = uint16(page)
	setHelpBinding(&state, helpCommandPageBinding(module, page))
	return EncodeState(state), module, moduleIndex, true
}

func helpRootPageFromState(state State, modules []HelpModule) (int, bool) {
	page := int(state.CategoryIndex)
	if helpBindingMatches(state, helpModulePageBinding(modules, page)) {
		_, _, page, _ = helpPageWindow(len(modules), page, HelpModuleSlotCount)
		return page, true
	}
	moduleIndex := int(state.CategoryIndex)
	if moduleIndex < 0 || moduleIndex >= len(modules) {
		return 0, false
	}
	module := modules[moduleIndex]
	commandPage := int(state.SettingIndex)
	if helpBindingMatches(state, helpCommandPageBinding(module, commandPage)) {
		return moduleIndex / HelpModuleSlotCount, true
	}
	commandIndex := int(state.SettingIndex)
	if commandIndex >= 0 && commandIndex < len(module.Commands) && helpBindingMatches(state, helpCommandDetailBinding(module, module.Commands[commandIndex])) {
		return moduleIndex / HelpModuleSlotCount, true
	}
	return 0, false
}

func helpPageCount(total, pageSize int) int {
	if pageSize <= 0 || total <= 0 {
		return 1
	}
	return (total + pageSize - 1) / pageSize
}

func helpPageWindow(total, page, pageSize int) (start, end, current, pages int) {
	pages = helpPageCount(total, pageSize)
	current = clampIndex(page, pages)
	start = current * pageSize
	if start > total {
		start = total
	}
	end = start + pageSize
	if end > total {
		end = total
	}
	return
}

func helpBinding(kind string, page int, identities []string) [bindingBytes]byte {
	var source strings.Builder
	source.WriteString("help\x00")
	source.WriteString(kind)
	source.WriteByte(0)
	source.WriteString(strconv.Itoa(page))
	for _, identity := range identities {
		source.WriteByte(0)
		source.WriteString(strings.ToLower(strings.TrimSpace(identity)))
	}
	sum := sha256.Sum256([]byte(source.String()))
	var binding [bindingBytes]byte
	copy(binding[:], sum[:bindingBytes])
	return binding
}

func setHelpBinding(state *State, binding [bindingBytes]byte) {
	clearSettingBinding(state)
	state.SettingBinding = binding
}

func helpBindingMatches(state State, binding [bindingBytes]byte) bool {
	return state.SchemaVersion == 0 && state.SettingBinding != ([bindingBytes]byte{}) && state.SettingBinding == binding
}

func helpModulePageBinding(modules []HelpModule, page int) [bindingBytes]byte {
	start, end, page, _ := helpPageWindow(len(modules), page, HelpModuleSlotCount)
	identities := make([]string, 0, end-start)
	for _, module := range modules[start:end] {
		identities = append(identities, module.Name)
	}
	return helpBinding("modules", page, identities)
}

func helpCommandPageBinding(module HelpModule, page int) [bindingBytes]byte {
	start, end, page, _ := helpPageWindow(len(module.Commands), page, HelpCommandSlotCount)
	identities := make([]string, 0, 1+end-start)
	identities = append(identities, module.Name)
	for _, command := range module.Commands[start:end] {
		identities = append(identities, command.Name)
	}
	return helpBinding("commands", page, identities)
}

func helpCommandDetailBinding(module HelpModule, command core.Command) [bindingBytes]byte {
	return helpBinding("detail", 0, []string{module.Name, command.Name})
}

type HelpModel struct {
	Commands []core.Command
	Page     int
	Locale   string
}

func HelpView(model HelpModel) presentation.View {
	locale := shellLocale(model.Locale)
	modules := HelpModules(model.Commands)
	start, end, page, pages := helpPageWindow(len(modules), model.Page, HelpModuleSlotCount)
	card := ui.NewCard(tr(locale, "assistant.help.title")).
		WithIcon("📚").
		WithHeader(tr(locale, "assistant.help.header")).
		AddField(tr(locale, "assistant.help.commands"), strconv.Itoa(len(model.Commands))).
		AddField(tr(locale, "assistant.help.modules"), strconv.Itoa(len(modules)))

	rows := make([]presentation.Row, 0, 5)
	if len(modules) == 0 {
		card.WithRaw(tr(locale, "assistant.help.empty"))
	} else {
		card.AddField(tr(locale, "assistant.help.position"), fmt.Sprintf("%d / %d", page+1, pages))
		for index := start; index < end; index += 2 {
			row := make(presentation.Row, 0, 2)
			for cursor := index; cursor < end && cursor < index+2; cursor++ {
				slot := cursor - start
				row = append(row, presentation.Button{
					Text:     truncateHelp(modules[cursor].Name, 32),
					ActionID: helpModuleSlotActions[slot],
				})
			}
			rows = append(rows, row)
		}
	}
	if pages > 1 {
		rows = append(rows, presentation.Row{
			{Text: tr(locale, "assistant.button.previous"), ActionID: ActionHelpPrev},
			{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome},
			{Text: tr(locale, "assistant.button.next"), ActionID: ActionHelpNext},
		})
	} else {
		rows = append(rows, presentation.Row{{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome}})
	}
	return presentation.View{Text: card.Render(), Rows: rows}
}

type HelpModuleModel struct {
	Module      HelpModule
	ModuleIndex int
	ModuleTotal int
	Page        int
	Locale      string
}

func HelpModuleView(model HelpModuleModel) presentation.View {
	locale := shellLocale(model.Locale)
	commands := model.Module.Commands
	name := strings.TrimSpace(model.Module.Name)
	if name == "" {
		name = "General"
	}
	start, end, page, pages := helpPageWindow(len(commands), model.Page, HelpCommandSlotCount)
	card := ui.NewCard(name).
		WithIcon("📂").
		WithHeader(tr(locale, "assistant.help.module_header")).
		AddField(tr(locale, "assistant.help.commands"), strconv.Itoa(len(commands)))
	if model.ModuleTotal > 0 {
		card.AddField(tr(locale, "assistant.help.module"), fmt.Sprintf("%d / %d", clampIndex(model.ModuleIndex, model.ModuleTotal)+1, model.ModuleTotal))
	}

	rows := make([]presentation.Row, 0, 5)
	if len(commands) == 0 {
		card.WithRaw(tr(locale, "assistant.help.module_empty"))
	} else {
		card.AddField(tr(locale, "assistant.help.position"), fmt.Sprintf("%d / %d", page+1, pages))
		for index := start; index < end; index += 2 {
			row := make(presentation.Row, 0, 2)
			for cursor := index; cursor < end && cursor < index+2; cursor++ {
				slot := cursor - start
				row = append(row, presentation.Button{
					Text:     "/" + truncateHelp(commands[cursor].Name, 30),
					ActionID: helpCommandSlotActions[slot],
				})
			}
			rows = append(rows, row)
		}
	}
	if pages > 1 {
		rows = append(rows, presentation.Row{
			{Text: tr(locale, "assistant.button.previous"), ActionID: ActionHelpCmdPrev},
			{Text: tr(locale, "assistant.button.modules"), ActionID: ActionHelp},
			{Text: tr(locale, "assistant.button.next"), ActionID: ActionHelpCmdNext},
		})
	} else {
		rows = append(rows, presentation.Row{{Text: tr(locale, "assistant.button.modules"), ActionID: ActionHelp}})
	}
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
