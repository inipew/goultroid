package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/settings"
)

func TestFeatureSpecAndViews(t *testing.T) {
	if err := ValidateSpec(); err != nil {
		t.Fatalf("ValidateSpec() error = %v", err)
	}
	spec := NewFeature().FeatureSpec()
	if len(spec.Interactions) != 44 {
		t.Fatalf("interactions = %d, want 44", len(spec.Interactions))
	}
	for _, screenID := range []string{
		InteractionHome,
		InteractionStatus,
		InteractionHelp,
		InteractionHelpModule,
		InteractionHelpCommand,
		InteractionSettings,
		InteractionLanguage,
		InteractionSettingsCategory,
		InteractionSettingDetail,
		InteractionSettingInput,
	} {
		interaction, ok := findInteraction(spec, feature.InteractionScreen, screenID)
		if !ok || !interaction.Surfaces.Supports(execution.SourceAssistant) || !interaction.Policy.PrivateOnly {
			t.Fatalf("screen %q = %+v, ok=%v", screenID, interaction, ok)
		}
	}

	state := InitialState()
	if got := RefreshCount(state); got != 0 {
		t.Fatalf("initial refresh count = %d", got)
	}
	state = NextRefreshState(state)
	if got := RefreshCount(state); got != 1 {
		t.Fatalf("refresh count = %d, want 1", got)
	}

	home := HomeView(HomeModel{Username: "TestBot", Uptime: time.Minute, Refreshes: 1})
	if len(home.Rows) != 3 || home.Rows[0][0].ActionID != ActionSettings || home.Rows[0][1].ActionID != ActionHelp {
		t.Fatalf("unexpected home actions: %+v", home.Rows)
	}
	status := StatusView(StatusModel{Username: "TestBot", Uptime: time.Minute, Refreshes: 1})
	if len(status.Rows) != 1 || status.Rows[0][0].ActionID != ActionStatusRefresh || status.Rows[0][1].ActionID != ActionHome {
		t.Fatalf("unexpected status actions: %+v", status.Rows)
	}
}

func TestHelpViewUsesDeterministicCanonicalNavigator(t *testing.T) {
	view := HelpView(HelpModel{Commands: []core.Command{
		{Name: "zeta", Category: "Zulu"},
		{Name: "alpha", Category: "Alpha"},
		{Name: "again", Category: "Alpha"},
	}, Selected: 0})
	if err := view.Validate(); err != nil {
		t.Fatalf("HelpView() invalid: %v", err)
	}
	if !strings.Contains(view.Text, "<b>Commands:</b> 3") || !strings.Contains(view.Text, "<b>Modules:</b> 2") {
		t.Fatalf("help summary missing canonical counts: %q", view.Text)
	}
	if !strings.Contains(view.Text, "Alpha · 2") {
		t.Fatalf("help selection is not deterministic: %q", view.Text)
	}
}

func TestSettingsViewsExposeOnlyTypedMutationsAndMaskSensitiveValues(t *testing.T) {
	home := SettingsHomeView(SettingsHomeModel{
		Category: SettingsCategory{ID: settings.CategorySecurity, Label: CategoryLabel(settings.CategorySecurity), Count: 2},
		Total:    2,
		Selected: 1,
	})
	if err := home.Validate(); err != nil {
		t.Fatalf("SettingsHomeView() invalid: %v", err)
	}
	if !strings.Contains(home.Text, "Security") || !strings.Contains(home.Text, "revision-fenced") {
		t.Fatalf("settings home text = %q", home.Text)
	}

	category := SettingsCategoryView(SettingsCategoryModel{
		Category: SettingsCategory{ID: settings.CategorySecurity, Label: CategoryLabel(settings.CategorySecurity), Count: 2},
		Current:  SettingSummary{Title: "API Token", Value: DisplaySettingValue(true, "secret")},
		Total:    2,
		Selected: 0,
	})
	if strings.Contains(category.Text, "secret") || !strings.Contains(category.Text, "••••") {
		t.Fatalf("sensitive category value leaked: %q", category.Text)
	}

	detail := SettingDetailView(SettingDetailModel{
		Definition: settings.SettingDefinition{
			Namespace:    "security",
			Key:          "token",
			Title:        "API Token",
			Description:  "Sensitive token",
			Type:         settings.TypeString,
			DefaultValue: "default-secret",
			Sensitive:    true,
		},
		Current: "runtime-secret",
		Source:  "User override",
	})
	if strings.Contains(detail.Text, "runtime-secret") || strings.Contains(detail.Text, "default-secret") {
		t.Fatalf("sensitive detail value leaked: %q", detail.Text)
	}
	if len(detail.Rows) == 0 || detail.Rows[0][0].ActionID != ActionSettingInput {
		t.Fatalf("string detail missing a2 input action: %+v", detail.Rows)
	}
	if strings.Contains(detail.Text, "Reset user override") {
		t.Fatalf("detail unexpectedly exposes reset without explicit override: %+v", detail.Rows)
	}
	input := SettingInputView(SettingInputModel{Definition: settings.SettingDefinition{
		Namespace: "security", Key: "token", Title: "API Token", Type: settings.TypeString, Sensitive: true,
	}})
	if err := input.Validate(); err != nil {
		t.Fatalf("SettingInputView() invalid: %v", err)
	}
	if !strings.Contains(input.Text, "2 minutes") || !strings.Contains(input.Text, "Sensitive values") || input.Rows[0][0].ActionID != ActionSettingInputCancel {
		t.Fatalf("unexpected input view: text=%q rows=%+v", input.Text, input.Rows)
	}
}

func TestStateCodecAcceptsHistoricalA2AndBoundsNavigator(t *testing.T) {
	legacy := make([]byte, v0StateBytes)
	legacy[7] = 4
	state := DecodeState(legacy)
	if state.Screen != ScreenHome || state.Refreshes != 4 {
		t.Fatalf("v0 state = %+v", state)
	}
	v1 := make([]byte, v1StateBytes)
	v1[0] = 1
	v1[1] = byte(ScreenSettingsCategory)
	v1[15] = 7
	state = DecodeState(v1)
	if state.Screen != ScreenSettingsCategory || state.Refreshes != 7 {
		t.Fatalf("v1 state = %+v", state)
	}
	v2 := make([]byte, v2StateBytes)
	v2[0] = 2
	v2[1] = byte(ScreenSettingDetail)
	v2Binding := SettingBinding("core", "prefix")
	copy(v2[16:32], v2Binding[:])
	state = DecodeState(v2)
	if state.Screen != ScreenSettingDetail || state.SchemaVersion != 0 {
		t.Fatalf("v2 state = %+v", state)
	}

	raw := StepCategoryState(InitialState(), 3, -1)
	state = DecodeState(raw)
	if state.Screen != ScreenSettings || state.CategoryIndex != 2 {
		t.Fatalf("previous category state = %+v", state)
	}
	raw = OpenCategoryState(raw, 3)
	raw = StepSettingState(raw, 2, 1)
	raw = OpenSettingState(raw, 2)
	state = DecodeState(raw)
	if state.Screen != ScreenSettingDetail || state.CategoryIndex != 2 || state.SettingIndex != 1 {
		t.Fatalf("detail state = %+v", state)
	}
	if len(raw) != stateBytes {
		t.Fatalf("encoded state bytes = %d, want %d", len(raw), stateBytes)
	}
	bound := BindSettingState(raw, "core", "prefix", 9)
	inputState := DecodeState(BeginSettingInputState(bound))
	if inputState.Screen != ScreenSettingInput || inputState.SchemaVersion != 9 || inputState.SettingBinding == ([bindingBytes]byte{}) {
		t.Fatalf("input state lost stable binding: %+v", inputState)
	}
	completed := DecodeState(CompleteSettingInputState(EncodeState(inputState)))
	if completed.Screen != ScreenSettingDetail || completed.SchemaVersion != 9 || completed.SettingBinding == ([bindingBytes]byte{}) {
		t.Fatalf("completed input state lost binding: %+v", completed)
	}
}

func findInteraction(spec feature.Spec, kind feature.InteractionKind, id string) (feature.Interaction, bool) {
	for _, interaction := range spec.Interactions {
		if interaction.Kind == kind && interaction.ID == id {
			return interaction, true
		}
	}
	return feature.Interaction{}, false
}
