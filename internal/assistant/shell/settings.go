package shell

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/ui"
)

type SettingsCategory struct {
	ID    string
	Label string
	Count int
}

type SettingsHomeModel struct {
	Category SettingsCategory
	Total    int
	Selected int
	Locale   string
}

func SettingsHomeView(model SettingsHomeModel) presentation.View {
	locale := shellLocale(model.Locale)
	card := ui.NewCard("GoUltroid Settings").
		WithIcon("⚙️").
		WithHeader(tr(locale, "assistant.settings.header")).
		AddField(tr(locale, "assistant.settings.categories"), strconv.Itoa(model.Total))

	rows := []presentation.Row{}
	if model.Total <= 0 {
		card.WithRaw(tr(locale, "assistant.settings.empty"))
	} else {
		selected := clampIndex(model.Selected, model.Total)
		label := strings.TrimSpace(model.Category.Label)
		if label == "" {
			label = model.Category.ID
		}
		card.AddField(tr(locale, "assistant.settings.selected"), fmt.Sprintf("%s · %d", ui.EscapeHTML(label), model.Category.Count)).
			AddField(tr(locale, "assistant.settings.position"), fmt.Sprintf("%d / %d", selected+1, model.Total))
		if model.Total > 1 {
			rows = append(rows, presentation.Row{
				{Text: tr(locale, "assistant.button.previous"), ActionID: ActionSettingsPrev},
				{Text: tr(locale, "assistant.button.open"), ActionID: ActionSettingsOpen},
				{Text: tr(locale, "assistant.button.next"), ActionID: ActionSettingsNext},
			})
		} else {
			rows = append(rows, presentation.Row{{Text: tr(locale, "assistant.button.open"), ActionID: ActionSettingsOpen}})
		}
	}
	card.WithFooter(tr(locale, "assistant.settings.footer"))
	rows = append(rows, presentation.Row{{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome}})
	return presentation.View{Text: card.Render(), Rows: rows}
}

type SettingSummary struct {
	Title string
	Value string
}

type SettingsCategoryModel struct {
	Category SettingsCategory
	Current  SettingSummary
	Total    int
	Selected int
	Locale   string
}

func SettingsCategoryView(model SettingsCategoryModel) presentation.View {
	locale := shellLocale(model.Locale)
	label := strings.TrimSpace(model.Category.Label)
	if label == "" {
		label = model.Category.ID
	}
	card := ui.NewCard(label).
		WithIcon("📂").
		WithHeader(tr(locale, "assistant.settings.category_header")).
		AddField(tr(locale, "assistant.settings.settings"), strconv.Itoa(model.Total))

	rows := []presentation.Row{}
	if model.Total <= 0 {
		card.WithRaw(tr(locale, "assistant.settings.category_empty"))
	} else {
		selected := clampIndex(model.Selected, model.Total)
		card.AddField(tr(locale, "assistant.settings.selected"), ui.EscapeHTML(model.Current.Title)).
			AddField(tr(locale, "assistant.settings.current"), model.Current.Value).
			AddField(tr(locale, "assistant.settings.position"), fmt.Sprintf("%d / %d", selected+1, model.Total))
		if model.Total > 1 {
			rows = append(rows, presentation.Row{
				{Text: tr(locale, "assistant.button.previous"), ActionID: ActionSettingPrev},
				{Text: tr(locale, "assistant.button.details"), ActionID: ActionSettingOpen},
				{Text: tr(locale, "assistant.button.next"), ActionID: ActionSettingNext},
			})
		} else {
			rows = append(rows, presentation.Row{{Text: tr(locale, "assistant.button.details"), ActionID: ActionSettingOpen}})
		}
	}
	rows = append(rows,
		presentation.Row{{Text: tr(locale, "assistant.settings.categories"), ActionID: ActionSettings}},
		presentation.Row{{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome}},
	)
	return presentation.View{Text: card.Render(), Rows: rows}
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
		presentation.Row{{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome}},
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
			{{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome}},
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
