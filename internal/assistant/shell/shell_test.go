package shell

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/settings"
)

func TestFeatureSpecAndViews(t *testing.T) {
	if err := ValidateSpec(); err != nil {
		t.Fatalf("ValidateSpec() error = %v", err)
	}
	spec := NewFeature().FeatureSpec()
	if len(spec.Interactions) != 76 {
		t.Fatalf("interactions = %d, want 76", len(spec.Interactions))
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
		InteractionSettingResetConfirm,
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
	if len(home.Rows) != 3 || home.Rows[0][0].ActionID != ActionLanguage || home.Rows[0][1].ActionID != ActionSettings {
		t.Fatalf("unexpected home actions: %+v", home.Rows)
	}
	status := StatusView(StatusModel{Username: "TestBot", Uptime: time.Minute, Refreshes: 1})
	if len(status.Rows) != 2 || len(status.Rows[0]) != 1 || status.Rows[0][0].ActionID != ActionStatusRefresh || len(status.Rows[1]) != 1 || status.Rows[1][0].ActionID != ActionHome {
		t.Fatalf("unexpected status actions: %+v", status.Rows)
	}
}

func TestHelpViewUsesDeterministicCanonicalDirectGrid(t *testing.T) {
	view := HelpView(HelpModel{Commands: []core.Command{
		{Name: "zeta", Category: "Zulu"},
		{Name: "alpha", Category: "Alpha"},
		{Name: "again", Category: "Alpha"},
	}, Page: 0})
	if err := view.Validate(); err != nil {
		t.Fatalf("HelpView() invalid: %v", err)
	}
	if !strings.Contains(view.Text, "<b>Commands:</b> 3") || !strings.Contains(view.Text, "<b>Modules:</b> 2") {
		t.Fatalf("help summary missing canonical counts: %q", view.Text)
	}
	if len(view.Rows) < 2 || len(view.Rows[0]) != 2 || view.Rows[0][0].Text != "Alpha" || view.Rows[0][1].Text != "Zulu" {
		t.Fatalf("help direct grid is not deterministic: %+v", view.Rows)
	}
	for _, actionID := range append(HelpModuleSlotActionIDs(), HelpCommandSlotActionIDs()...) {
		interaction, ok := findInteraction(NewFeature().FeatureSpec(), feature.InteractionAction, actionID)
		if !ok || !interaction.Policy.PrivateOnly {
			t.Fatalf("help slot interaction %q = %+v, ok=%v", actionID, interaction, ok)
		}
	}
}

func TestSettingsViewsUseDeterministicCanonicalDirectGrid(t *testing.T) {
	categories := []SettingsCategory{
		{ID: settings.CategoryGeneral, Label: CategoryLabel(settings.CategoryGeneral)},
		{ID: settings.CategorySecurity, Label: CategoryLabel(settings.CategorySecurity)},
		{ID: settings.CategoryUI, Label: CategoryLabel(settings.CategoryUI)},
	}
	home := SettingsHomeView(SettingsHomeModel{Categories: categories, Page: 0})
	if err := home.Validate(); err != nil {
		t.Fatalf("SettingsHomeView() invalid: %v", err)
	}
	if len(home.Rows) != 3 || len(home.Rows[0]) != 2 ||
		home.Rows[0][0].ActionID != settingsCategorySlotActions[0] ||
		home.Rows[0][1].ActionID != settingsCategorySlotActions[1] {
		t.Fatalf("settings category grid = %+v", home.Rows)
	}

	defs := []settings.SettingDefinition{
		{Namespace: "core", Key: "prefix", Title: "Command Prefix", Category: settings.CategoryGeneral, Type: settings.TypeString},
		{Namespace: "ui", Key: "locale", Title: "Language", Category: settings.CategoryGeneral, Type: settings.TypeEnum, AllowedValues: []string{"en", "id"}},
	}
	category := SettingsCategoryView(SettingsCategoryModel{
		Category:    categories[0],
		Definitions: defs,
		Page:        0,
	})
	if err := category.Validate(); err != nil {
		t.Fatalf("SettingsCategoryView() invalid: %v", err)
	}
	if len(category.Rows) != 2 || len(category.Rows[0]) != 2 ||
		category.Rows[0][0].Text != "Command Prefix" ||
		category.Rows[0][1].Text != "Language" ||
		category.Rows[0][0].ActionID != settingSlotActions[0] ||
		category.Rows[0][1].ActionID != settingSlotActions[1] {
		t.Fatalf("settings definition grid = %+v", category.Rows)
	}
	if strings.Contains(category.Text, "Current") {
		t.Fatalf("category grid unexpectedly rendered effective values: %q", category.Text)
	}

	for _, actionID := range append(SettingsCategorySlotActionIDs(), SettingSlotActionIDs()...) {
		interaction, ok := findInteraction(NewFeature().FeatureSpec(), feature.InteractionAction, actionID)
		if !ok || !interaction.Policy.PrivateOnly {
			t.Fatalf("settings slot interaction %q = %+v, ok=%v", actionID, interaction, ok)
		}
	}
}

func TestSettingsGridStateIsBoundedAndRejectsCatalogRemap(t *testing.T) {
	categories := make([]SettingsCategory, 0, 10)
	for i := 0; i < 10; i++ {
		categories = append(categories, SettingsCategory{
			ID:    fmt.Sprintf("cat%02d", i),
			Label: fmt.Sprintf("Category %02d", i),
		})
	}
	root := SettingsHomeState(InitialState(), categories, true)
	state, ok := StepSettingsCategoryPageState(root, categories, 1)
	if !ok || DecodeState(state).CategoryIndex != 1 {
		t.Fatalf("next settings category page = %+v ok=%v", DecodeState(state), ok)
	}
	category, categoryIndex, ok := ResolveSettingsCategorySlot(state, categories, 0)
	if !ok || categoryIndex != 8 || category.ID != "cat08" {
		t.Fatalf("category slot = index:%d category:%+v ok=%v", categoryIndex, category, ok)
	}

	defs := make([]settings.SettingDefinition, 0, 10)
	for i := 0; i < 10; i++ {
		defs = append(defs, settings.SettingDefinition{
			Namespace: "feature",
			Key:       fmt.Sprintf("item%02d", i),
			Title:     fmt.Sprintf("Item %02d", i),
			Category:  category.ID,
			Type:      settings.TypeString,
		})
	}
	state = SettingsCategoryState(state, categoryIndex, category, defs, 0)
	state, ok = StepSettingPageState(state, category, defs, 1)
	if !ok || DecodeState(state).SettingIndex != 1 {
		t.Fatalf("next setting page = %+v ok=%v", DecodeState(state), ok)
	}
	def, settingIndex, ok := ResolveSettingSlot(state, category, defs, 1)
	if !ok || settingIndex != 9 || def.Key != "item09" {
		t.Fatalf("setting slot = index:%d def:%+v ok=%v", settingIndex, def, ok)
	}

	detail := SettingDetailState(state, settingIndex)
	detail = BindSettingState(detail, def.Namespace, def.Key, 9)
	if !SettingBindingMatches(detail, "feature", "item09") || DecodeState(detail).SchemaVersion != 9 {
		t.Fatalf("detail binding = %+v", DecodeState(detail))
	}
	back := BackSettingsCategoryState(detail, categoryIndex, category, defs, settingIndex)
	if DecodeState(back).Screen != ScreenSettingsCategory || DecodeState(back).SettingIndex != 1 {
		t.Fatalf("back category page = %+v", DecodeState(back))
	}

	changedCategories := append([]SettingsCategory{{ID: "aaa", Label: "AAA"}}, categories...)
	if _, _, ok := ResolveSettingsCategorySlot(root, changedCategories, 0); ok {
		t.Fatal("category slot remapped after registry change instead of failing stale")
	}
	categoryState := SettingsCategoryState(InitialState(), 0, categories[0], defs, 0)
	changedDefs := append([]settings.SettingDefinition{{
		Namespace: "aaa", Key: "aaa", Title: "AAA", Category: categories[0].ID, Type: settings.TypeString,
	}}, defs...)
	if _, _, ok := ResolveSettingSlot(categoryState, categories[0], changedDefs, 0); ok {
		t.Fatal("setting slot remapped after definition change instead of failing stale")
	}

	schemaDefs := append([]settings.SettingDefinition(nil), defs...)
	schemaState := SettingsCategoryState(InitialState(), 0, categories[0], schemaDefs, 0)
	schemaDefs[0].Title = "Changed schema title"
	if _, _, ok := ResolveSettingSlot(schemaState, categories[0], schemaDefs, 0); ok {
		t.Fatal("setting slot accepted a same-key schema replacement instead of failing stale")
	}
}

func TestSettingsViewsExposeOnlyTypedMutationsAndMaskSensitiveValues(t *testing.T) {
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

func TestStateCodecAcceptsHistoricalA2AndDetailBinding(t *testing.T) {
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

	raw := EncodeState(State{Screen: ScreenSettingDetail, CategoryIndex: 2, SettingIndex: 1})
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

func TestOwnerNavigationLifetimePolicy(t *testing.T) {
	if InteractionTTL != 24*time.Hour {
		t.Fatalf("InteractionTTL = %v, want 24h", InteractionTTL)
	}
	if SettingsInputTTL != 2*time.Minute {
		t.Fatalf("SettingsInputTTL = %v, want 2m", SettingsInputTTL)
	}
}

func TestSettingResetConfirmViewRequiresExplicitConfirmation(t *testing.T) {
	view := SettingResetConfirmView(SettingResetConfirmModel{
		Definition: settings.SettingDefinition{Namespace: "core", Key: "prefix", Title: "Command prefix"},
	})
	if !shellViewHasAction(view.Rows, ActionSettingResetConfirm) || !shellViewHasAction(view.Rows, ActionSettingResetCancel) {
		t.Fatalf("reset confirmation rows=%+v", view.Rows)
	}
	if shellViewHasAction(view.Rows, ActionSettingReset) {
		t.Fatalf("reset confirmation unexpectedly loops to reset opener: %+v", view.Rows)
	}
}

func shellViewHasAction(rows []presentation.Row, actionID string) bool {
	for _, row := range rows {
		for _, button := range row {
			if button.ActionID == actionID {
				return true
			}
		}
	}
	return false
}
