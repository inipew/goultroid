package shell

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/ui"
)

const (
	SettingsCategorySlotCount = 8
	SettingSlotCount          = 8
)

var settingsCategorySlotActions = [SettingsCategorySlotCount]string{
	"settings_category_slot_0", "settings_category_slot_1", "settings_category_slot_2", "settings_category_slot_3",
	"settings_category_slot_4", "settings_category_slot_5", "settings_category_slot_6", "settings_category_slot_7",
}

var settingSlotActions = [SettingSlotCount]string{
	"setting_slot_0", "setting_slot_1", "setting_slot_2", "setting_slot_3",
	"setting_slot_4", "setting_slot_5", "setting_slot_6", "setting_slot_7",
}

func SettingsCategorySlotActionIDs() []string {
	return append([]string(nil), settingsCategorySlotActions[:]...)
}

func SettingSlotActionIDs() []string {
	return append([]string(nil), settingSlotActions[:]...)
}

type SettingsCategory struct {
	ID    string
	Label string
	Count int
}

func SettingsHomeState(raw []byte, categories []SettingsCategory, reset bool) []byte {
	state := DecodeState(raw)
	page := 0
	if !reset {
		switch state.Screen {
		case ScreenSettings:
			current := int(state.CategoryIndex)
			if settingsPageBindingMatches(state, settingsCategoryPageBinding(categories, current)) {
				page = current
			}
		case ScreenSettingsCategory, ScreenSettingDetail, ScreenSettingInput:
			index := int(state.CategoryIndex)
			if index >= 0 && index < len(categories) {
				page = index / SettingsCategorySlotCount
			}
		}
	}
	_, _, page, _ = settingsPageWindow(len(categories), page, SettingsCategorySlotCount)
	state.Screen = ScreenSettings
	state.CategoryIndex = uint16(page)
	state.SettingIndex = 0
	setSettingsPageBinding(&state, settingsCategoryPageBinding(categories, page))
	return EncodeState(state)
}

func StepSettingsCategoryPageState(raw []byte, categories []SettingsCategory, delta int) ([]byte, bool) {
	state := DecodeState(raw)
	page := int(state.CategoryIndex)
	if state.Screen != ScreenSettings || !settingsPageBindingMatches(state, settingsCategoryPageBinding(categories, page)) {
		return nil, false
	}
	page = stepIndex(page, settingsPageCount(len(categories), SettingsCategorySlotCount), delta)
	state.CategoryIndex = uint16(page)
	state.SettingIndex = 0
	setSettingsPageBinding(&state, settingsCategoryPageBinding(categories, page))
	return EncodeState(state), true
}

func ResolveSettingsCategorySlot(raw []byte, categories []SettingsCategory, slot int) (SettingsCategory, int, bool) {
	state := DecodeState(raw)
	page := int(state.CategoryIndex)
	if slot < 0 || slot >= SettingsCategorySlotCount || state.Screen != ScreenSettings ||
		!settingsPageBindingMatches(state, settingsCategoryPageBinding(categories, page)) {
		return SettingsCategory{}, 0, false
	}
	start, end, _, _ := settingsPageWindow(len(categories), page, SettingsCategorySlotCount)
	index := start + slot
	if index < start || index >= end {
		return SettingsCategory{}, 0, false
	}
	return categories[index], index, true
}

func SettingsCategoryState(raw []byte, categoryIndex int, category SettingsCategory, defs []settings.SettingDefinition, page int) []byte {
	state := DecodeState(raw)
	_, _, page, _ = settingsPageWindow(len(defs), page, SettingSlotCount)
	state.Screen = ScreenSettingsCategory
	state.CategoryIndex = uint16(categoryIndex)
	state.SettingIndex = uint16(page)
	setSettingsPageBinding(&state, settingsDefinitionPageBinding(category.ID, defs, page))
	return EncodeState(state)
}

func StepSettingPageState(raw []byte, category SettingsCategory, defs []settings.SettingDefinition, delta int) ([]byte, bool) {
	state := DecodeState(raw)
	page := int(state.SettingIndex)
	if state.Screen != ScreenSettingsCategory ||
		!settingsPageBindingMatches(state, settingsDefinitionPageBinding(category.ID, defs, page)) {
		return nil, false
	}
	page = stepIndex(page, settingsPageCount(len(defs), SettingSlotCount), delta)
	state.SettingIndex = uint16(page)
	setSettingsPageBinding(&state, settingsDefinitionPageBinding(category.ID, defs, page))
	return EncodeState(state), true
}

func ResolveSettingSlot(raw []byte, category SettingsCategory, defs []settings.SettingDefinition, slot int) (settings.SettingDefinition, int, bool) {
	state := DecodeState(raw)
	page := int(state.SettingIndex)
	if slot < 0 || slot >= SettingSlotCount || state.Screen != ScreenSettingsCategory ||
		!settingsPageBindingMatches(state, settingsDefinitionPageBinding(category.ID, defs, page)) {
		return settings.SettingDefinition{}, 0, false
	}
	start, end, _, _ := settingsPageWindow(len(defs), page, SettingSlotCount)
	index := start + slot
	if index < start || index >= end {
		return settings.SettingDefinition{}, 0, false
	}
	return defs[index], index, true
}

func SettingDetailState(raw []byte, settingIndex int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenSettingDetail
	state.SettingIndex = uint16(settingIndex)
	clearSettingBinding(&state)
	return EncodeState(state)
}

func BackSettingsCategoryState(raw []byte, categoryIndex int, category SettingsCategory, defs []settings.SettingDefinition, settingIndex int) []byte {
	page := 0
	if settingIndex > 0 {
		page = settingIndex / SettingSlotCount
	}
	return SettingsCategoryState(raw, categoryIndex, category, defs, page)
}

func settingsPageCount(total, pageSize int) int {
	if pageSize <= 0 || total <= 0 {
		return 1
	}
	return (total + pageSize - 1) / pageSize
}

func settingsPageWindow(total, page, pageSize int) (start, end, current, pages int) {
	pages = settingsPageCount(total, pageSize)
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

func settingsPageBinding(kind string, page int, identities []string) [bindingBytes]byte {
	var source strings.Builder
	source.WriteString("settings\x00")
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

func setSettingsPageBinding(state *State, binding [bindingBytes]byte) {
	clearSettingBinding(state)
	state.SettingBinding = binding
}

func settingsPageBindingMatches(state State, binding [bindingBytes]byte) bool {
	return state.SchemaVersion == 0 &&
		state.SettingBinding != ([bindingBytes]byte{}) &&
		state.SettingBinding == binding
}

func settingsCategoryPageBinding(categories []SettingsCategory, page int) [bindingBytes]byte {
	start, end, page, _ := settingsPageWindow(len(categories), page, SettingsCategorySlotCount)
	identities := make([]string, 0, end-start)
	for _, category := range categories[start:end] {
		identities = append(identities, category.ID)
	}
	return settingsPageBinding("categories", page, identities)
}

func settingsDefinitionPageBinding(category string, defs []settings.SettingDefinition, page int) [bindingBytes]byte {
	start, end, page, _ := settingsPageWindow(len(defs), page, SettingSlotCount)
	identities := make([]string, 0, 1+end-start)
	identities = append(identities, category)
	for _, def := range defs[start:end] {
		identities = append(identities, settingDefinitionSlotIdentity(def))
	}
	return settingsPageBinding("definitions", page, identities)
}

func settingDefinitionSlotIdentity(def settings.SettingDefinition) string {
	minValue := ""
	if def.MinVal != nil {
		minValue = strconv.FormatInt(*def.MinVal, 10)
	}
	maxValue := ""
	if def.MaxVal != nil {
		maxValue = strconv.FormatInt(*def.MaxVal, 10)
	}
	return strings.Join([]string{
		def.Namespace,
		def.Key,
		string(def.Type),
		def.DefaultValue,
		strings.Join(def.AllowedValues, "\x1f"),
		minValue,
		maxValue,
		def.Title,
		def.Description,
		def.Category,
		string(def.UI.Widget),
		strconv.FormatInt(def.UI.Step, 10),
		strings.Join(def.UI.Presets, "\x1f"),
		strconv.FormatBool(def.UI.Confirm),
		strconv.FormatBool(def.UI.Searchable),
		strconv.Itoa(def.Order),
		strconv.FormatBool(def.Sensitive),
	}, "\x1e")
}

type SettingsHomeModel struct {
	Categories []SettingsCategory
	Page       int
	Locale     string
}

func SettingsHomeView(model SettingsHomeModel) presentation.View {
	locale := shellLocale(model.Locale)
	start, end, page, pages := settingsPageWindow(len(model.Categories), model.Page, SettingsCategorySlotCount)
	card := ui.NewCard("GoUltroid Settings").
		WithIcon("⚙️").
		WithHeader(tr(locale, "assistant.settings.header")).
		AddField(tr(locale, "assistant.settings.categories"), strconv.Itoa(len(model.Categories)))

	rows := make([]presentation.Row, 0, 5)
	if len(model.Categories) == 0 {
		card.WithRaw(tr(locale, "assistant.settings.empty"))
	} else {
		if pages > 1 {
			card.AddField(tr(locale, "assistant.settings.position"), fmt.Sprintf("%d / %d", page+1, pages))
		}
		for index := start; index < end; index += 2 {
			row := make(presentation.Row, 0, 2)
			for cursor := index; cursor < end && cursor < index+2; cursor++ {
				slot := cursor - start
				label := strings.TrimSpace(model.Categories[cursor].Label)
				if label == "" {
					label = model.Categories[cursor].ID
				}
				row = append(row, presentation.Button{
					Text:     truncateSettingsLabel(label, 32),
					ActionID: settingsCategorySlotActions[slot],
				})
			}
			rows = append(rows, row)
		}
	}
	if pages > 1 {
		rows = append(rows, presentation.Row{
			{Text: tr(locale, "assistant.button.previous"), ActionID: ActionSettingsPrev},
			{Text: tr(locale, "assistant.button.next"), ActionID: ActionSettingsNext},
		})
	}
	rows = append(rows, presentation.Row{
		{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome},
		{Text: "✖ Close", ActionID: ActionClose},
	})
	card.WithFooter(tr(locale, "assistant.settings.footer"))
	return presentation.View{Text: card.Render(), Rows: rows}
}

type SettingsCategoryModel struct {
	Category    SettingsCategory
	Definitions []settings.SettingDefinition
	Page        int
	Locale      string
}

func SettingsCategoryView(model SettingsCategoryModel) presentation.View {
	locale := shellLocale(model.Locale)
	label := strings.TrimSpace(model.Category.Label)
	if label == "" {
		label = model.Category.ID
	}
	start, end, page, pages := settingsPageWindow(len(model.Definitions), model.Page, SettingSlotCount)
	card := ui.NewCard(label).
		WithIcon("📂").
		WithHeader(tr(locale, "assistant.settings.category_header")).
		AddField(tr(locale, "assistant.settings.settings"), strconv.Itoa(len(model.Definitions)))

	rows := make([]presentation.Row, 0, 5)
	if len(model.Definitions) == 0 {
		card.WithRaw(tr(locale, "assistant.settings.category_empty"))
	} else {
		if pages > 1 {
			card.AddField(tr(locale, "assistant.settings.position"), fmt.Sprintf("%d / %d", page+1, pages))
		}
		for index := start; index < end; index += 2 {
			row := make(presentation.Row, 0, 2)
			for cursor := index; cursor < end && cursor < index+2; cursor++ {
				slot := cursor - start
				def := LocalizedSettingDefinition(locale, model.Definitions[cursor])
				title := strings.TrimSpace(def.Title)
				if title == "" {
					title = def.Namespace + ":" + def.Key
				}
				row = append(row, presentation.Button{
					Text:     truncateSettingsLabel(title, 32),
					ActionID: settingSlotActions[slot],
				})
			}
			rows = append(rows, row)
		}
	}
	if pages > 1 {
		rows = append(rows, presentation.Row{
			{Text: tr(locale, "assistant.button.previous"), ActionID: ActionSettingPrev},
			{Text: tr(locale, "assistant.settings.categories"), ActionID: ActionSettings},
			{Text: tr(locale, "assistant.button.next"), ActionID: ActionSettingNext},
		})
	} else {
		rows = append(rows, presentation.Row{{Text: tr(locale, "assistant.settings.categories"), ActionID: ActionSettings}})
	}
	rows = append(rows, presentation.Row{
		{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome},
		{Text: "✖ Close", ActionID: ActionClose},
	})
	return presentation.View{Text: card.Render(), Rows: rows}
}

func truncateSettingsLabel(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

type SettingDetailModel struct {
	Definition   settings.SettingDefinition
	Current      string
	Source       string
	ExplicitUser bool
	Notice       string
	Locale       string
}

func SettingDetailView(model SettingDetailModel) presentation.View {
	locale := shellLocale(model.Locale)
	def := LocalizedSettingDefinition(locale, model.Definition)
	title := strings.TrimSpace(def.Title)
	if title == "" {
		title = def.Namespace + ":" + def.Key
	}
	description := strings.TrimSpace(def.Description)
	if description == "" {
		description = tr(locale, "assistant.settings.no_description")
	}
	current := displaySettingValue(def.Sensitive, model.Current)
	defaultValue := displaySettingValue(def.Sensitive, def.DefaultValue)
	source := strings.TrimSpace(model.Source)
	if source == "" {
		source = tr(locale, "assistant.settings.inherited")
	}
	card := ui.NewCard(title).
		WithIcon("⚙️").
		WithHeader(ui.EscapeHTML(description)).
		AddField(tr(locale, "assistant.settings.key"), ui.Code(def.Namespace+":"+def.Key)).
		AddField(tr(locale, "assistant.settings.current"), current).
		AddField(tr(locale, "assistant.settings.source"), ui.EscapeHTML(localizedSettingSource(locale, source))).
		AddField(tr(locale, "assistant.settings.type"), ui.Code(string(def.Type))).
		AddField(tr(locale, "assistant.settings.default"), defaultValue).
		AddField(tr(locale, "assistant.settings.widget"), ui.Code(string(def.UI.Widget)))

	if len(def.AllowedValues) > 0 {
		card.AddField(tr(locale, "assistant.settings.options"), ui.Code(boundedOptions(def.AllowedValues, 8)))
	}
	if def.MinVal != nil || def.MaxVal != nil {
		card.AddField(tr(locale, "assistant.settings.range"), boundText(def.MinVal)+" … "+boundText(def.MaxVal))
	}
	if def.UI.Step > 0 {
		card.AddField(tr(locale, "assistant.settings.step"), ui.Code(strconv.FormatInt(def.UI.Step, 10)))
	}
	if notice := strings.TrimSpace(model.Notice); notice != "" {
		card.AddField(tr(locale, "assistant.settings.result"), ui.EscapeHTML(localizedNotice(locale, notice)))
	}

	rows := []presentation.Row{}
	switch def.Type {
	case settings.TypeBool, settings.TypeEnum:
		rows = append(rows, presentation.Row{{Text: tr(locale, "assistant.settings.change"), ActionID: ActionSettingChange}})
	case settings.TypeInt, settings.TypeDuration:
		rows = append(rows, presentation.Row{
			{Text: tr(locale, "assistant.settings.decrease"), ActionID: ActionSettingDecrease},
			{Text: tr(locale, "assistant.settings.increase"), ActionID: ActionSettingIncrease},
		})
	case settings.TypeString:
		rows = append(rows, presentation.Row{{Text: tr(locale, "assistant.settings.change"), ActionID: ActionSettingInput}})
		card.WithRaw(tr(locale, "assistant.settings.freeform"))
	}
	if model.ExplicitUser {
		rows = append(rows, presentation.Row{{Text: tr(locale, "assistant.settings.reset"), ActionID: ActionSettingReset}})
	}
	card.WithFooter(tr(locale, "assistant.settings.detail_footer"))
	rows = append(rows,
		presentation.Row{{Text: tr(locale, "assistant.settings.category"), ActionID: ActionSettingBack}, {Text: tr(locale, "assistant.settings.categories"), ActionID: ActionSettings}},
		presentation.Row{{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome}, {Text: "✖ Close", ActionID: ActionClose}},
	)
	return presentation.View{Text: card.Render(), Rows: rows}
}

type SettingInputModel struct {
	Definition settings.SettingDefinition
	Notice     string
	Locale     string
}

func SettingInputView(model SettingInputModel) presentation.View {
	locale := shellLocale(model.Locale)
	def := LocalizedSettingDefinition(locale, model.Definition)
	title := strings.TrimSpace(def.Title)
	if title == "" {
		title = def.Namespace + ":" + def.Key
	}
	description := strings.TrimSpace(def.Description)
	if description == "" {
		description = tr(locale, "assistant.settings.input_header")
	}
	card := ui.NewCard(title).
		WithIcon("✏️").
		WithHeader(ui.EscapeHTML(description)).
		AddField(tr(locale, "assistant.settings.key"), ui.Code(def.Namespace+":"+def.Key)).
		AddField(tr(locale, "assistant.settings.input"), tr(locale, "assistant.settings.input_prompt")).
		AddField(tr(locale, "assistant.settings.cancel"), ui.Code("/cancel")).
		AddField(tr(locale, "assistant.settings.expires"), tr(locale, "assistant.settings.two_minutes"))

	if def.Sensitive {
		card.AddField(tr(locale, "assistant.settings.privacy"), tr(locale, "assistant.settings.privacy_text"))
	}
	if notice := strings.TrimSpace(model.Notice); notice != "" {
		card.AddField(tr(locale, "assistant.settings.result"), ui.EscapeHTML(localizedNotice(locale, notice)))
	}
	card.WithFooter(tr(locale, "assistant.settings.input_footer"))

	return presentation.View{
		Text: card.Render(),
		Rows: []presentation.Row{
			{{Text: tr(locale, "assistant.settings.cancel_input"), ActionID: ActionSettingInputCancel}},
			{{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome}, {Text: "✖ Close", ActionID: ActionClose}},
		},
	}
}

func CategoryLabel(category string, locales ...string) string {
	locale := optionalLocale(locales)
	switch strings.ToLower(strings.TrimSpace(category)) {
	case settings.CategoryGeneral:
		return tr(locale, "assistant.settings.general")
	case settings.CategorySecurity:
		return tr(locale, "assistant.settings.security")
	case settings.CategoryModeration:
		return tr(locale, "assistant.settings.moderation")
	case settings.CategoryAutomation:
		return tr(locale, "assistant.settings.automation")
	case settings.CategoryUI:
		return tr(locale, "assistant.settings.interface")
	case settings.CategoryAdvanced:
		return tr(locale, "assistant.settings.advanced")
	default:
		category = strings.TrimSpace(category)
		if category == "" {
			category = tr(locale, "assistant.settings.other")
		}
		return "📁 " + category
	}
}

func DisplaySettingValue(sensitive bool, value string, locales ...string) string {
	locale := optionalLocale(locales)
	if sensitive && value != "" {
		return "••••"
	}
	if value == "" {
		return tr(locale, "assistant.settings.empty_value")
	}
	return ui.Code(value)
}

func boundedOptions(values []string, limit int) string {
	if limit <= 0 || len(values) == 0 {
		return ""
	}
	if len(values) <= limit {
		return strings.Join(values, ", ")
	}
	return strings.Join(values[:limit], ", ") + fmt.Sprintf(", … +%d", len(values)-limit)
}

func displaySettingValue(sensitive bool, value string) string {
	if sensitive && value != "" {
		return "••••"
	}
	return ui.EscapeHTML(value)
}

func boundText(value *int64) string {
	if value == nil {
		return "∞"
	}
	return strconv.FormatInt(*value, 10)
}

type SettingResetConfirmModel struct {
	Definition settings.SettingDefinition
	Locale     string
}

func SettingResetConfirmView(model SettingResetConfirmModel) presentation.View {
	locale := shellLocale(model.Locale)
	def := LocalizedSettingDefinition(locale, model.Definition)
	title := strings.TrimSpace(def.Title)
	if title == "" {
		title = def.Namespace + ":" + def.Key
	}
	card := ui.NewCard(title).
		WithIcon("⚠️").
		WithHeader("Reset this user override?").
		AddField(tr(locale, "assistant.settings.key"), ui.Code(def.Namespace+":"+def.Key)).
		WithRaw("This removes the user override and restores the inherited/default value. This action does not run until you confirm.")
	return presentation.View{
		Text: card.Render(),
		Rows: []presentation.Row{
			{
				{Text: "✅ Confirm reset", ActionID: ActionSettingResetConfirm},
				{Text: "↩ Cancel", ActionID: ActionSettingResetCancel},
			},
			{
				{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome},
				{Text: "✖ Close", ActionID: ActionClose},
			},
		},
	}
}
