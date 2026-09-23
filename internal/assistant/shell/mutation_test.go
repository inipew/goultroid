package shell

import (
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/settings"
)

func TestPlanMutationTypedOperations(t *testing.T) {
	boolDef := settings.SettingDefinition{Namespace: "feature", Key: "enabled", Type: settings.TypeBool}
	plan, err := PlanMutation(boolDef, "false", false, MutationChange)
	if err != nil || plan.Outcome != MutationChanged || plan.Value != "true" {
		t.Fatalf("bool plan = %+v err=%v", plan, err)
	}

	enumDef := settings.SettingDefinition{
		Namespace: "ui", Key: "mode", Type: settings.TypeEnum,
		AllowedValues: []string{"compact", "full"},
	}
	plan, err = PlanMutation(enumDef, "full", false, MutationChange)
	if err != nil || plan.Value != "compact" {
		t.Fatalf("enum plan = %+v err=%v", plan, err)
	}

	min, max := int64(0), int64(10)
	intDef := settings.SettingDefinition{
		Namespace: "core", Key: "count", Type: settings.TypeInt,
		MinVal: &min, MaxVal: &max, UI: settings.UIHint{Step: 5},
	}
	plan, err = PlanMutation(intDef, "10", false, MutationIncrease)
	if err != nil || plan.Value != "0" {
		t.Fatalf("int wrap plan = %+v err=%v", plan, err)
	}

	plan, err = PlanMutation(boolDef, "false", false, MutationReset)
	if err != nil || plan.Outcome != MutationNoop {
		t.Fatalf("reset noop = %+v err=%v", plan, err)
	}
	plan, err = PlanMutation(boolDef, "false", true, MutationReset)
	if err != nil || plan.Outcome != MutationChanged {
		t.Fatalf("reset changed = %+v err=%v", plan, err)
	}
}

func TestPlanMutationRejectsStringAndWrongOperation(t *testing.T) {
	stringDef := settings.SettingDefinition{Namespace: "core", Key: "prefix", Type: settings.TypeString}
	if _, err := PlanMutation(stringDef, ".", false, MutationChange); !errors.Is(err, ErrMutationUnsupported) {
		t.Fatalf("string mutation error = %v, want %v", err, ErrMutationUnsupported)
	}
	boolDef := settings.SettingDefinition{Namespace: "feature", Key: "enabled", Type: settings.TypeBool}
	if _, err := PlanMutation(boolDef, "false", false, MutationIncrease); !errors.Is(err, ErrMutationUnsupported) {
		t.Fatalf("bool increment error = %v, want %v", err, ErrMutationUnsupported)
	}
}

func TestSettingBindingStableAndVersioned(t *testing.T) {
	raw := BindSettingState(EncodeState(State{Screen: ScreenSettingDetail}), " Core ", "Prefix", 9)
	if !SettingBindingMatches(raw, "core", "prefix") {
		t.Fatal("normalized binding did not match")
	}
	if got := DecodeState(raw).SchemaVersion; got != 9 {
		t.Fatalf("schema version = %d, want 9", got)
	}
	if SettingBindingMatches(raw, "core", "other") {
		t.Fatal("different stable key matched binding")
	}
	raw = ScreenState(raw, ScreenSettingsCategory)
	state := DecodeState(raw)
	if SettingBindingMatches(raw, "core", "prefix") || state.SchemaVersion != 0 {
		t.Fatalf("binding survived leaving detail screen: %+v", state)
	}
}

func TestSafeMutationValueMasksSensitiveResultData(t *testing.T) {
	def := settings.SettingDefinition{Sensitive: true}
	if got := SafeMutationValue(def, "secret"); got != "••••" {
		t.Fatalf("masked value = %q", got)
	}
	if got := SafeMutationValue(def, ""); got != "" {
		t.Fatalf("empty sensitive value = %q", got)
	}
	if got := SafeMutationValue(settings.SettingDefinition{}, "visible"); got != "visible" {
		t.Fatalf("non-sensitive value = %q", got)
	}
}
