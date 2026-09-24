package shell

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	appstatus "github.com/inipew/goultroid/internal/application/status"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/ui"
)

const (
	FeatureID = "assistant_shell"

	InteractionTTL = 24 * time.Hour

	InteractionStart            = "start"
	InteractionHome             = "home"
	InteractionStatus           = "status"
	InteractionHelp             = "help"
	InteractionSettings         = "settings"
	InteractionLanguage         = "language"
	InteractionSettingsCategory = "settings_category"
	InteractionSettingDetail    = "setting_detail"
	InteractionSettingInput     = "setting_input"
	InteractionInlineRoot       = "inline_root"
	InteractionInlineHelp       = "inline_help"
	InteractionInlinePing       = "inline_ping"

	ActionRefresh            = "refresh"
	ActionPing               = "ping"
	ActionStatus             = "status"
	ActionHelp               = "help"
	ActionHome               = "home"
	ActionStatusRefresh      = "status_refresh"
	ActionSettings           = "settings"
	ActionLanguage           = "language"
	ActionLanguageEnglish    = "language_en"
	ActionLanguageIndonesian = "language_id"
	ActionSettingsPrev       = "settings_prev"
	ActionSettingsNext       = "settings_next"
	ActionSettingPrev        = "setting_prev"
	ActionSettingNext        = "setting_next"
	ActionSettingBack        = "setting_back"
	ActionSettingChange      = "setting_change"
	ActionSettingDecrease    = "setting_dec"
	ActionSettingIncrease    = "setting_inc"
	ActionSettingReset       = "setting_reset"
	ActionSettingInput       = "setting_input"
	ActionSettingInputCancel = "setting_input_cancel"
)

// Feature is the first production feature migrated onto the interaction
// foundation. It owns metadata and stateless inline handlers, but no goroutine
// or Telegram transport resource.
type Feature struct {
	inlineCatalog feature.Catalog
	settingsSvc   *settings.Service
	startTime     time.Time
}

func NewFeature() *Feature { return &Feature{} }

func (f *Feature) SetInlineCatalog(catalog feature.Catalog) {
	if f != nil {
		f.inlineCatalog = catalog
	}
}

func (f *Feature) SetStartTime(startTime time.Time) {
	if f != nil {
		f.startTime = startTime
	}
}

func (f *Feature) SetSettingsService(svc *settings.Service) {
	if f != nil {
		f.settingsSvc = svc
	}
}

func (*Feature) Name() string             { return FeatureID }
func (*Feature) Commands() []core.Command { return nil }
func (*Feature) Init() error              { return nil }

func (*Feature) FeatureSpec() feature.Spec {
	assistant := execution.SurfaceAssistant
	inlineSurface := execution.SurfaceInline
	startPolicy := feature.PublicPolicy(assistant)
	startPolicy.PrivateOnly = true
	ownerPolicy := feature.OwnerPolicy(assistant)
	ownerPolicy.PrivateOnly = true
	inlinePublicPolicy := feature.PublicPolicy(inlineSurface)
	inlineOwnerPolicy := feature.OwnerPolicy(inlineSurface)

	spec := feature.Spec{
		ID:          FeatureID,
		Name:        "Assistant Shell",
		Description: "Root Assistant control surface migrated to interaction protocol a2.",
		Category:    "Assistant",
		Interactions: []feature.Interaction{
			{ID: InteractionStart, Kind: feature.InteractionDeepLink, Description: "Telegram /start entry point", Surfaces: assistant, Policy: startPolicy},
			{ID: InteractionInlineRoot, Kind: feature.InteractionInline, Description: "Default inline Assistant discovery surface", Surfaces: inlineSurface, Policy: inlinePublicPolicy},
			{ID: InteractionInlineHelp, Kind: feature.InteractionInline, Description: "Feature-catalog generated inline help", Surfaces: inlineSurface, Policy: inlineOwnerPolicy},
			{ID: InteractionInlinePing, Kind: feature.InteractionInline, Description: "Public inline Assistant liveness", Surfaces: inlineSurface, Policy: inlinePublicPolicy},
			{ID: InteractionHome, Kind: feature.InteractionScreen, Description: "Owner root/home screen", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionStatus, Kind: feature.InteractionScreen, Description: "Read-only Assistant runtime status", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionHelp, Kind: feature.InteractionScreen, Description: "Read-only Assistant command overview", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionHelpModule, Kind: feature.InteractionScreen, Description: "Canonical Assistant module command navigator", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionHelpCommand, Kind: feature.InteractionScreen, Description: "Canonical Assistant command detail", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionSettings, Kind: feature.InteractionScreen, Description: "Settings category navigator", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionLanguage, Kind: feature.InteractionScreen, Description: "Canonical Assistant locale selector", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionSettingsCategory, Kind: feature.InteractionScreen, Description: "Settings value navigator", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionSettingDetail, Kind: feature.InteractionScreen, Description: "Bound setting detail and typed mutation surface", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionSettingInput, Kind: feature.InteractionScreen, Description: "Bound free-form setting input surface", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionRefresh, Kind: feature.InteractionAction, Description: "Refresh shell state and presentation", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionPing, Kind: feature.InteractionAction, Description: "Acknowledge shell liveness", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionStatus, Kind: feature.InteractionAction, Description: "Navigate to read-only status", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelp, Kind: feature.InteractionAction, Description: "Navigate to read-only help overview", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelpPrev, Kind: feature.InteractionAction, Description: "Open previous Assistant help module page", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelpNext, Kind: feature.InteractionAction, Description: "Open next Assistant help module page", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelpCmdPrev, Kind: feature.InteractionAction, Description: "Open previous command page in help module", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelpCmdNext, Kind: feature.InteractionAction, Description: "Open next command page in help module", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelpBack, Kind: feature.InteractionAction, Description: "Return from command detail to its module", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHome, Kind: feature.InteractionAction, Description: "Return to the shell home screen", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionStatusRefresh, Kind: feature.InteractionAction, Description: "Refresh read-only status", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettings, Kind: feature.InteractionAction, Description: "Navigate to settings categories", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionLanguage, Kind: feature.InteractionAction, Description: "Navigate to canonical Assistant language selection", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionLanguageEnglish, Kind: feature.InteractionAction, Description: "Set canonical Assistant locale to English", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionLanguageIndonesian, Kind: feature.InteractionAction, Description: "Set canonical Assistant locale to Indonesian", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingsPrev, Kind: feature.InteractionAction, Description: "Select previous settings category", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingsNext, Kind: feature.InteractionAction, Description: "Select next settings category", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingPrev, Kind: feature.InteractionAction, Description: "Select previous setting", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingNext, Kind: feature.InteractionAction, Description: "Select next setting", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingBack, Kind: feature.InteractionAction, Description: "Return to selected settings category", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingChange, Kind: feature.InteractionAction, Description: "Apply typed bool or enum mutation", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingDecrease, Kind: feature.InteractionAction, Description: "Decrease typed numeric or duration setting", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingIncrease, Kind: feature.InteractionAction, Description: "Increase typed numeric or duration setting", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingReset, Kind: feature.InteractionAction, Description: "Reset bound user setting override", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingInput, Kind: feature.InteractionAction, Description: "Begin bounded free-form input for a bound string setting", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingInputCancel, Kind: feature.InteractionAction, Description: "Cancel bounded free-form setting input", Surfaces: assistant, Policy: ownerPolicy},
		},
	}
	for _, actionID := range HelpModuleSlotActionIDs() {
		spec.Interactions = append(spec.Interactions, feature.Interaction{
			ID:          actionID,
			Kind:        feature.InteractionAction,
			Description: "Open one bounded Assistant help module slot",
			Surfaces:    assistant,
			Policy:      ownerPolicy,
		})
	}
	for _, actionID := range HelpCommandSlotActionIDs() {
		spec.Interactions = append(spec.Interactions, feature.Interaction{
			ID:          actionID,
			Kind:        feature.InteractionAction,
			Description: "Open one bounded Assistant help command slot",
			Surfaces:    assistant,
			Policy:      ownerPolicy,
		})
	}
	for _, actionID := range SettingsCategorySlotActionIDs() {
		spec.Interactions = append(spec.Interactions, feature.Interaction{
			ID:          actionID,
			Kind:        feature.InteractionAction,
			Description: "Open one bounded Assistant settings category slot",
			Surfaces:    assistant,
			Policy:      ownerPolicy,
		})
	}
	for _, actionID := range SettingSlotActionIDs() {
		spec.Interactions = append(spec.Interactions, feature.Interaction{
			ID:          actionID,
			Kind:        feature.InteractionAction,
			Description: "Open one bounded Assistant setting slot",
			Surfaces:    assistant,
			Policy:      ownerPolicy,
		})
	}
	return spec
}

type HomeModel struct {
	Username  string
	Uptime    time.Duration
	Refreshes uint64
	Locale    string
}

func HomeView(model HomeModel) presentation.View {
	locale := shellLocale(model.Locale)
	username := normalizedUsername(model.Username)
	greeting := fmt.Sprintf("Hey @%s. Please browse through the options", username)
	if locale == "id" {
		greeting = fmt.Sprintf("Haloo @%s. Silakan telusuri opsi", username)
	}
	text := "<b>GoUltroid Assistant</b>\n\n" + greeting

	return presentation.View{
		Text: text,
		Rows: []presentation.Row{
			{{Text: tr(locale, "assistant.button.language"), ActionID: ActionLanguage}, {Text: tr(locale, "assistant.button.settings"), ActionID: ActionSettings}},
			{{Text: tr(locale, "assistant.button.status"), ActionID: ActionStatus}, {Text: tr(locale, "assistant.button.help"), ActionID: ActionHelp}},
			{{Text: tr(locale, "assistant.button.ping"), ActionID: ActionPing}, {Text: tr(locale, "assistant.button.refresh"), ActionID: ActionRefresh}},
		},
	}
}

type StatusModel struct {
	Username  string
	Uptime    time.Duration
	Engine    string
	Refreshes uint64
	Locale    string
}

func StatusView(model StatusModel) presentation.View {
	locale := shellLocale(model.Locale)
	username := normalizedUsername(model.Username)
	engine := strings.TrimSpace(model.Engine)
	if engine == "" {
		engine = "GoUltroid (MTProto)"
	}
	text := fmt.Sprintf(
		"<b>%s</b>\n\n<b>%s</b> - @%s\n<b>%s</b> - %s\n<b>%s</b> - %s\n<b>%s</b> - %s\n<b>%s</b> - %s",
		tr(locale, "assistant.status.title"),
		tr(locale, "assistant.field.assistant"),
		username,
		tr(locale, "assistant.field.status"),
		tr(locale, "assistant.status.operational"),
		tr(locale, "assistant.field.uptime"),
		appstatus.FormatDuration(model.Uptime),
		tr(locale, "assistant.field.engine"),
		ui.EscapeHTML(engine),
		tr(locale, "assistant.field.callbacks"),
		tr(locale, "assistant.status.active"),
	)
	if model.Refreshes > 0 {
		text += fmt.Sprintf("\n<b>%s</b> - %s", tr(locale, "assistant.field.refreshes"), strconv.FormatUint(model.Refreshes, 10))
	}

	return presentation.View{
		Text: text,
		Rows: []presentation.Row{
			{{Text: tr(locale, "assistant.button.refresh"), ActionID: ActionStatusRefresh}},
			{{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome}},
		},
	}
}

func normalizedUsername(username string) string {
	username = strings.TrimSpace(username)
	if username == "" {
		return "GoUltroidBot"
	}
	return username
}

func ValidateSpec() error {
	spec := NewFeature().FeatureSpec()
	bound, err := feature.BindCanonicalCommands(spec, nil)
	if err != nil {
		return err
	}
	if bound.ID != FeatureID {
		return fmt.Errorf("assistant shell feature id = %q", bound.ID)
	}
	for name, view := range map[string]presentation.View{
		"home":         HomeView(HomeModel{}),
		"status":       StatusView(StatusModel{}),
		"language":     LanguageView(LanguageModel{}),
		"help":         HelpView(HelpModel{}),
		"help_module":  HelpModuleView(HelpModuleModel{}),
		"help_command": HelpCommandView(HelpCommandModel{}),
		"settings":     SettingsHomeView(SettingsHomeModel{}),
		"category":     SettingsCategoryView(SettingsCategoryModel{}),
		"detail":       SettingDetailView(SettingDetailModel{}),
		"input":        SettingInputView(SettingInputModel{}),
	} {
		if err := view.Validate(); err != nil {
			return fmt.Errorf("%s view: %w", name, err)
		}
	}
	return nil
}
