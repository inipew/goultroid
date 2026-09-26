package settings

import (
	"context"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/services/callback"
	settingsservice "github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/ui"
)

func TestP4NativeSettingsNavigationOwnsAllCallbacks(t *testing.T) {
	p, _, _, _ := setupTestPlugin(t)
	ctx := context.Background()
	base := MenuState{
		Scope:   settingsservice.ScopeGlobal,
		OwnerID: 42,
		ChatID:  -10042,
		Page:    1,
	}

	home := p.renderHomeScreen(ctx, base)
	assertSettingsOwnedCallbacks(t, home)
	assertSettingsNavigationLabels(t, home, "❌ Close")
	if hasSettingsNavigationLabel(home, "« Back to Menu") {
		t.Fatal("native settings home still exposes Assistant-owned Back to Menu navigation")
	}

	categories := p.service.Registry().Categories()
	if len(categories) == 0 {
		t.Fatal("settings registry has no categories")
	}
	categoryState := base
	var defs []settingsservice.SettingDefinition
	for _, categoryID := range categories {
		defs = p.service.Registry().ListByCategory(categoryID)
		if len(defs) > 0 {
			categoryState.Category = categoryID
			break
		}
	}
	if len(defs) == 0 {
		t.Fatal("settings registry has no populated categories")
	}
	category := p.renderCategoryScreen(ctx, categoryState)
	assertSettingsOwnedCallbacks(t, category)
	assertSettingsNavigationLabels(t, category, "🏠 Home", "❌ Close")

	detailState := categoryState
	detailState.SetTarget(defs[0].Namespace, defs[0].Key)
	detail := p.renderSettingDetailScreen(ctx, detailState)
	assertSettingsOwnedCallbacks(t, detail)
	if !hasSettingsNavigationPrefix(detail, "🔙 Back to ") {
		t.Fatal("native settings detail has no route back to its category")
	}
}

func assertSettingsOwnedCallbacks(t *testing.T, screen *ui.Screen) {
	t.Helper()
	if screen == nil {
		t.Fatal("settings screen is nil")
	}
	for _, row := range screen.Rows {
		for _, button := range row {
			if button.Type != ui.ButtonCallback {
				continue
			}
			namespace, _, _, err := callback.ParseCallbackData(button.Data)
			if err != nil {
				t.Fatalf("settings button %q has invalid callback data %q: %v", button.Text, button.Data, err)
			}
			if namespace != "settings" {
				t.Fatalf("settings button %q crosses into callback namespace %q", button.Text, namespace)
			}
		}
	}
}

func assertSettingsNavigationLabels(t *testing.T, screen *ui.Screen, labels ...string) {
	t.Helper()
	for _, label := range labels {
		if !hasSettingsNavigationLabel(screen, label) {
			t.Fatalf("settings screen %q missing navigation %q", screen.ID, label)
		}
	}
}

func hasSettingsNavigationLabel(screen *ui.Screen, label string) bool {
	if screen == nil {
		return false
	}
	for _, row := range screen.Rows {
		for _, button := range row {
			if button.Text == label {
				return true
			}
		}
	}
	return false
}

func hasSettingsNavigationPrefix(screen *ui.Screen, prefix string) bool {
	if screen == nil {
		return false
	}
	for _, row := range screen.Rows {
		for _, button := range row {
			if strings.HasPrefix(button.Text, prefix) {
				return true
			}
		}
	}
	return false
}
