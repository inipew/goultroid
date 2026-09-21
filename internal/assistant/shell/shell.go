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
	"github.com/inipew/goultroid/internal/ui"
)

const (
	FeatureID = "assistant_shell"

	InteractionStart            = "start"
	InteractionHome             = "home"
	InteractionStatus           = "status"
	InteractionHelp             = "help"
	InteractionSettings         = "settings"
	InteractionSettingsCategory = "settings_category"
	InteractionSettingDetail    = "setting_detail"
	InteractionSettingInput     = "setting_input"

	ActionRefresh            = "refresh"
	ActionPing               = "ping"
	ActionStatus             = "status"
	ActionHelp               = "help"
	ActionHome               = "home"
	ActionStatusRefresh      = "status_refresh"
	ActionSettings           = "settings"
	ActionSettingsPrev       = "settings_prev"
	ActionSettingsNext       = "settings_next"
	ActionSettingsOpen       = "settings_open"
	ActionSettingPrev        = "setting_prev"
	ActionSettingNext        = "setting_next"
	ActionSettingOpen        = "setting_open"
	ActionSettingBack        = "setting_back"
	ActionSettingChange      = "setting_change"
	ActionSettingDecrease    = "setting_dec"
	ActionSettingIncrease    = "setting_inc"
	ActionSettingReset       = "setting_reset"
	ActionSettingInput       = "setting_input"
	ActionSettingInputCancel = "setting_input_cancel"
)

// Feature is the first production feature migrated onto the P0-P4 interaction
// foundation. It intentionally owns no goroutine or transport resource.
type Feature struct{}

func NewFeature() *Feature { return &Feature{} }

func (*Feature) Name() string             { return FeatureID }
func (*Feature) Commands() []core.Command { return nil }
func (*Feature) Init() error              { return nil }

func (*Feature) FeatureSpec() feature.Spec {
	assistant := execution.SurfaceAssistant
	startPolicy := feature.PublicPolicy(assistant)
	startPolicy.PrivateOnly = true
	ownerPolicy := feature.OwnerPolicy(assistant)
	ownerPolicy.PrivateOnly = true

	return feature.Spec{
		ID:          FeatureID,
		Name:        "Assistant Shell",
		Description: "Root Assistant control surface migrated to interaction protocol a2.",
		Category:    "Assistant",
		Interactions: []feature.Interaction{
			{ID: InteractionStart, Kind: feature.InteractionDeepLink, Description: "Telegram /start entry point", Surfaces: assistant, Policy: startPolicy},
			{ID: InteractionHome, Kind: feature.InteractionScreen, Description: "Owner root/home screen", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionStatus, Kind: feature.InteractionScreen, Description: "Read-only Assistant runtime status", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionHelp, Kind: feature.InteractionScreen, Description: "Read-only Assistant command overview", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionHelpModule, Kind: feature.InteractionScreen, Description: "Canonical Assistant module command navigator", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionHelpCommand, Kind: feature.InteractionScreen, Description: "Canonical Assistant command detail", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionSettings, Kind: feature.InteractionScreen, Description: "Settings category navigator", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionSettingsCategory, Kind: feature.InteractionScreen, Description: "Settings value navigator", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionSettingDetail, Kind: feature.InteractionScreen, Description: "Bound setting detail and typed mutation surface", Surfaces: assistant, Policy: ownerPolicy},
			{ID: InteractionSettingInput, Kind: feature.InteractionScreen, Description: "Bound free-form setting input surface", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionRefresh, Kind: feature.InteractionAction, Description: "Refresh shell state and presentation", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionPing, Kind: feature.InteractionAction, Description: "Acknowledge shell liveness", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionStatus, Kind: feature.InteractionAction, Description: "Navigate to read-only status", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelp, Kind: feature.InteractionAction, Description: "Navigate to read-only help overview", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelpPrev, Kind: feature.InteractionAction, Description: "Select previous Assistant help module", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelpNext, Kind: feature.InteractionAction, Description: "Select next Assistant help module", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelpOpen, Kind: feature.InteractionAction, Description: "Open selected Assistant help module", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelpCmdPrev, Kind: feature.InteractionAction, Description: "Select previous command in help module", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelpCmdNext, Kind: feature.InteractionAction, Description: "Select next command in help module", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelpCmdOpen, Kind: feature.InteractionAction, Description: "Open selected Assistant command detail", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHelpBack, Kind: feature.InteractionAction, Description: "Return from command detail to its module", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionHome, Kind: feature.InteractionAction, Description: "Return to the shell home screen", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionStatusRefresh, Kind: feature.InteractionAction, Description: "Refresh read-only status", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettings, Kind: feature.InteractionAction, Description: "Navigate to settings categories", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingsPrev, Kind: feature.InteractionAction, Description: "Select previous settings category", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingsNext, Kind: feature.InteractionAction, Description: "Select next settings category", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingsOpen, Kind: feature.InteractionAction, Description: "Open selected settings category", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingPrev, Kind: feature.InteractionAction, Description: "Select previous setting", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingNext, Kind: feature.InteractionAction, Description: "Select next setting", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingOpen, Kind: feature.InteractionAction, Description: "Open selected setting detail", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingBack, Kind: feature.InteractionAction, Description: "Return to selected settings category", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingChange, Kind: feature.InteractionAction, Description: "Apply typed bool or enum mutation", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingDecrease, Kind: feature.InteractionAction, Description: "Decrease typed numeric or duration setting", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingIncrease, Kind: feature.InteractionAction, Description: "Increase typed numeric or duration setting", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingReset, Kind: feature.InteractionAction, Description: "Reset bound user setting override", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingInput, Kind: feature.InteractionAction, Description: "Begin bounded free-form input for a bound string setting", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionSettingInputCancel, Kind: feature.InteractionAction, Description: "Cancel bounded free-form setting input", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionClose, Kind: feature.InteractionAction, Description: "Close and delete the current Assistant shell message", Surfaces: assistant, Policy: ownerPolicy},
		},
	}
}

type HomeModel struct {
	Username  string
	Uptime    time.Duration
	Refreshes uint64
}

func HomeView(model HomeModel) presentation.View {
	username := normalizedUsername(model.Username)
	card := ui.NewCard("GoUltroid Assistant").
		WithIcon("🤖").
		WithHeader("Control center for your userbot and assistant.").
		AddField("Bot", "@"+username).
		AddField("Status", "🟢 Online & ready").
		AddField("Uptime", appstatus.FormatDuration(model.Uptime))
	if model.Refreshes > 0 {
		card.AddField("Session refreshes", strconv.FormatUint(model.Refreshes, 10))
	}
	card.WithFooter("<i>Assistant shell navigation and state are fully a2-native.</i>")

	return presentation.View{
		Text: card.Render(),
		Rows: []presentation.Row{
			{{Text: "⚙️ Settings", ActionID: ActionSettings}, {Text: "📚 Help", ActionID: ActionHelp}},
			{{Text: "📊 Status", ActionID: ActionStatus}, {Text: "🔄 Refresh", ActionID: ActionRefresh}},
			{{Text: "🏓 Ping", ActionID: ActionPing}, {Text: "❌ Close", ActionID: ActionClose}},
		},
	}
}

type StatusModel struct {
	Username  string
	Uptime    time.Duration
	Engine    string
	Refreshes uint64
}

func StatusView(model StatusModel) presentation.View {
	username := normalizedUsername(model.Username)
	engine := strings.TrimSpace(model.Engine)
	if engine == "" {
		engine = "GoUltroid (MTProto)"
	}
	card := ui.NewCard("System Status").
		WithIcon("📊").
		WithHeader("Assistant runtime health and transport information.").
		AddField("Assistant", "@"+username).
		AddField("Status", "🟢 Operational").
		AddField("Uptime", appstatus.FormatDuration(model.Uptime)).
		AddField("Engine", ui.EscapeHTML(engine)).
		AddField("Callbacks", "🟢 Active")
	if model.Refreshes > 0 {
		card.AddField("Session refreshes", strconv.FormatUint(model.Refreshes, 10))
	}
	card.WithFooter("<i>Refresh reads the latest runtime state without creating a new session.</i>")

	return presentation.View{
		Text: card.Render(),
		Rows: []presentation.Row{
			{{Text: "🔄 Refresh", ActionID: ActionStatusRefresh}, {Text: "🏠 Home", ActionID: ActionHome}},
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
