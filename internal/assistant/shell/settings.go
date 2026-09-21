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
}

func SettingsHomeView(model SettingsHomeModel) presentation.View {
	card := ui.NewCard("GoUltroid Settings").
		WithIcon("⚙️").
		WithHeader("Settings browser backed by the central settings registry.").
		AddField("Categories", strconv.Itoa(model.Total))

	rows := []presentation.Row{}
	if model.Total <= 0 {
		card.WithRaw("<i>No settings are currently registered.</i>")
	} else {
		selected := clampIndex(model.Selected, model.Total)
		label := strings.TrimSpace(model.Category.Label)
		if label == "" {
			label = model.Category.ID
		}
		card.AddField("Selected", fmt.Sprintf("%s · %d settings", ui.EscapeHTML(label), model.Category.Count)).
			AddField("Position", fmt.Sprintf("%d / %d", selected+1, model.Total))
		if model.Total > 1 {
			rows = append(rows, presentation.Row{
				{Text: "◀️ Previous", ActionID: ActionSettingsPrev},
				{Text: "Open", ActionID: ActionSettingsOpen},
				{Text: "Next ▶️", ActionID: ActionSettingsNext},
			})
		} else {
			rows = append(rows, presentation.Row{{Text: "Open", ActionID: ActionSettingsOpen}})
		}
	}
	card.WithFooter("<i>Typed mutations are available from a bound setting detail; text input remains in Classic menu.</i>")
	rows = append(rows, presentation.Row{
		{Text: "🏠 Home", ActionID: ActionHome},
		{Text: "🧭 Classic menu", ActionID: ActionLegacy},
	})
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
}

func SettingsCategoryView(model SettingsCategoryModel) presentation.View {
	label := strings.TrimSpace(model.Category.Label)
	if label == "" {
		label = model.Category.ID
	}
	card := ui.NewCard(label).
		WithIcon("📂").
		WithHeader("Browse effective values in this settings category.").
		AddField("Settings", strconv.Itoa(model.Total))

	rows := []presentation.Row{}
	if model.Total <= 0 {
		card.WithRaw("<i>No settings are currently registered in this category.</i>")
	} else {
		selected := clampIndex(model.Selected, model.Total)
		card.AddField("Selected", ui.EscapeHTML(model.Current.Title)).
			AddField("Current", model.Current.Value).
			AddField("Position", fmt.Sprintf("%d / %d", selected+1, model.Total))
		if model.Total > 1 {
			rows = append(rows, presentation.Row{
				{Text: "◀️ Previous", ActionID: ActionSettingPrev},
				{Text: "Details", ActionID: ActionSettingOpen},
				{Text: "Next ▶️", ActionID: ActionSettingNext},
			})
		} else {
			rows = append(rows, presentation.Row{{Text: "Details", ActionID: ActionSettingOpen}})
		}
	}
	rows = append(rows,
		presentation.Row{{Text: "⚙️ Categories", ActionID: ActionSettings}},
		presentation.Row{{Text: "🏠 Home", ActionID: ActionHome}},
	)
	return presentation.View{Text: card.Render(), Rows: rows}
}

type SettingDetailModel struct {
	Definition   settings.SettingDefinition
	Current      string
	Source       string
	ExplicitUser bool
	Notice       string
}

func SettingDetailView(model SettingDetailModel) presentation.View {
	def := model.Definition
	title := strings.TrimSpace(def.Title)
	if title == "" {
		title = def.Namespace + ":" + def.Key
	}
	description := strings.TrimSpace(def.Description)
	if description == "" {
		description = "No description is registered for this setting."
	}
	current := displaySettingValue(def.Sensitive, model.Current)
	defaultValue := displaySettingValue(def.Sensitive, def.DefaultValue)
	source := strings.TrimSpace(model.Source)
	if source == "" {
		source = "Inherited/default"
	}
	card := ui.NewCard(title).
		WithIcon("⚙️").
		WithHeader(ui.EscapeHTML(description)).
		AddField("Key", ui.Code(def.Namespace+":"+def.Key)).
		AddField("Current", current).
		AddField("Source", ui.EscapeHTML(source)).
		AddField("Type", ui.Code(string(def.Type))).
		AddField("Default", defaultValue).
		AddField("Widget", ui.Code(string(def.UI.Widget)))

	if len(def.AllowedValues) > 0 {
		card.AddField("Options", ui.Code(boundedOptions(def.AllowedValues, 8)))
	}
	if def.MinVal != nil || def.MaxVal != nil {
		card.AddField("Range", boundText(def.MinVal)+" … "+boundText(def.MaxVal))
	}
	if def.UI.Step > 0 {
		card.AddField("Step", ui.Code(strconv.FormatInt(def.UI.Step, 10)))
	}
	if notice := strings.TrimSpace(model.Notice); notice != "" {
		card.AddField("Result", ui.EscapeHTML(notice))
	}

	rows := []presentation.Row{}
	switch def.Type {
	case settings.TypeBool, settings.TypeEnum:
		rows = append(rows, presentation.Row{{Text: "✏️ Change", ActionID: ActionSettingChange}})
	case settings.TypeInt, settings.TypeDuration:
		rows = append(rows, presentation.Row{
			{Text: "➖ Decrease", ActionID: ActionSettingDecrease},
			{Text: "➕ Increase", ActionID: ActionSettingIncrease},
		})
	case settings.TypeString:
		card.WithRaw("<i>Text input still uses Classic menu.</i>")
	}
	if model.ExplicitUser {
		rows = append(rows, presentation.Row{{Text: "↩ Reset user override", ActionID: ActionSettingReset}})
	}
	card.WithFooter("<i>Mutation is bound to this stable setting identity and consumes the current session revision before persistence.</i>")
	rows = append(rows,
		presentation.Row{{Text: "« Category", ActionID: ActionSettingBack}, {Text: "⚙️ Categories", ActionID: ActionSettings}},
		presentation.Row{{Text: "🏠 Home", ActionID: ActionHome}},
	)
	return presentation.View{Text: card.Render(), Rows: rows}
}

func CategoryLabel(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case settings.CategoryGeneral:
		return "⚙️ General"
	case settings.CategorySecurity:
		return "🔒 Security"
	case settings.CategoryModeration:
		return "🛡 Moderation"
	case settings.CategoryAutomation:
		return "🤖 Automation"
	case settings.CategoryUI:
		return "🎨 Interface"
	case settings.CategoryAdvanced:
		return "🧰 Advanced"
	default:
		category = strings.TrimSpace(category)
		if category == "" {
			category = "Other"
		}
		return "📁 " + category
	}
}

func DisplaySettingValue(sensitive bool, value string) string {
	if sensitive && value != "" {
		return "••••"
	}
	if value == "" {
		return "<i>empty</i>"
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

func boundText(value *int64) string {
	if value == nil {
		return "∞"
	}
	return strconv.FormatInt(*value, 10)
}
