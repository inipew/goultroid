package settings

import (
	"context"
	"testing"
)

func TestMigration_ActionValueColon(t *testing.T) {
	p, svc, _ := setupTestPlugin(t)
	ctx := context.Background()

	state := MenuState{
		Scope:       "global",
		ScopeID:     0,
		Target:      &SettingTarget{Namespace: "core", Key: "prefix"},
		ActionValue: "a:b:c",
		OwnerID:     12345,
	}
	if err := p.applySettingMutation(ctx, 12345, &state, nativeIntentSet); err != nil {
		t.Fatalf("apply colon value: %v", err)
	}
	val, _ := svc.Resolve(ctx, 12345, 0, "core", "prefix")
	if val != "a:b:c" {
		t.Fatalf("expected colon value preserved, got %q", val)
	}
}

func TestMigration_NewStateNoSelectedValueEmbedding(t *testing.T) {
	p, _, _ := setupTestPlugin(t)
	ctx := context.Background()
	state := MenuState{
		Scope:   "global",
		ScopeID: 0,
		OwnerID: 12345,
		ChatID:  -100123,
	}
	state.SetTarget("pmpermit", "max_warns")
	state.ActionValue = "5"
	if state.Selected != "pmpermit:max_warns" {
		t.Fatalf("Selected should be ns:key only, got %q", state.Selected)
	}
	if state.ActionValue != "5" {
		t.Fatalf("ActionValue should hold value, got %q", state.ActionValue)
	}

	detail := p.renderSettingDetailScreen(ctx, MenuState{
		Scope:    "global",
		ScopeID:  0,
		Target:   &SettingTarget{Namespace: "pmpermit", Key: "max_warns"},
		Selected: "pmpermit:max_warns",
		OwnerID:  12345,
		Category: "security",
	})
	if detail == nil {
		t.Fatal("detail screen nil")
	}
}
