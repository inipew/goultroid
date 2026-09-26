package settings

import (
	"context"
	"strings"
	"testing"

	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	settingsservice "github.com/inipew/goultroid/internal/settings"
)

func TestP1F2SharedSettingsViewOwnsSessionBoundActions(t *testing.T) {
	p, _, _ := setupTestPlugin(t)
	ctx := context.Background()
	base := MenuState{
		Scope:   settingsservice.ScopeGlobal,
		OwnerID: 42,
		ChatID:  -10042,
		Page:    1,
	}

	raw, view, err := p.nativeSettingsView(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || len(view.Rows) == 0 {
		t.Fatal("native settings home has no bounded state/actions")
	}
	for _, row := range view.Rows {
		for _, button := range row {
			if !strings.HasPrefix(button.ActionID, "slot_") {
				t.Fatalf("native action id=%q, want session slot", button.ActionID)
			}
		}
	}
	if strings.Contains(view.Text, "v1:") {
		t.Fatalf("settings view leaked legacy callback protocol: %q", view.Text)
	}

	_, assistantView, err := p.assistantSettingsView(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range assistantView.Rows {
		for _, button := range row {
			if !strings.HasPrefix(button.ActionID, "assistant_slot_") {
				t.Fatalf("assistant action id=%q, want assistant session slot", button.ActionID)
			}
		}
	}
}

func TestP1F2SettingsStateRemainsTransportNeutral(t *testing.T) {
	state := nativeSettingsState{
		Menu: MenuState{
			Scope:   settingsservice.ScopeGlobal,
			OwnerID: 42,
			ChatID:  -10042,
			Page:    1,
		},
		Slots: []nativeSettingsIntent{{Kind: nativeIntentHome}},
	}
	raw, err := encodeNativeSettingsState(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "v1:") || strings.Contains(string(raw), "a2:") {
		t.Fatalf("server-side settings state leaked transport token: %s", raw)
	}
	if rootinteraction.OwnsCallbackData(raw) {
		t.Fatal("server-side state must not be interpreted as callback data")
	}
}
