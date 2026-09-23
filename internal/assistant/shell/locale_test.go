package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/settings"
)

func TestLanguageViewUsesTypedA2Actions(t *testing.T) {
	view := LanguageView(LanguageModel{Locale: "id"})
	if !strings.Contains(view.Text, "Bahasa Assistant") || !strings.Contains(view.Text, "Bahasa Indonesia") {
		t.Fatalf("Indonesian language view=%q", view.Text)
	}
	seen := map[string]bool{}
	for _, row := range view.Rows {
		for _, button := range row {
			seen[button.ActionID] = true
		}
	}
	for _, action := range []string{ActionLanguageEnglish, ActionLanguageIndonesian, ActionHome} {
		if !seen[action] {
			t.Fatalf("language view missing typed action %q", action)
		}
	}
}

func TestHomeAndStatusViewsAreLocaleAware(t *testing.T) {
	home := HomeView(HomeModel{Username: "goultroid", Uptime: time.Minute, Locale: "id"})
	if !strings.Contains(home.Text, "Pusat kontrol") {
		t.Fatalf("localized home=%q", home.Text)
	}
	if !viewContainsButton(home, "⚙️ Pengaturan") || !viewContainsButton(home, "🌐 Bahasa") {
		t.Fatalf("localized home buttons=%+v", home.Rows)
	}

	status := StatusView(StatusModel{Username: "goultroid", Locale: "id"})
	if !strings.Contains(status.Text, "Status Sistem") || !strings.Contains(status.Text, "Beroperasi") {
		t.Fatalf("localized status=%q", status.Text)
	}
}

func TestLocaleSettingMetadataUsesLocalizedAssistantPresentation(t *testing.T) {
	def := settings.SettingDefinition{
		Namespace:   LocaleSettingNamespace,
		Key:         LocaleSettingKey,
		Type:        settings.TypeEnum,
		Title:       "Assistant Language",
		Description: "Language used by the Assistant control interface",
	}
	localized := LocalizedSettingDefinition("id", def)
	if localized.Title != "Bahasa Assistant" {
		t.Fatalf("localized title=%q", localized.Title)
	}
	if !strings.Contains(localized.Description, "Bahasa") {
		t.Fatalf("localized description=%q", localized.Description)
	}
	if got := CategoryLabel(settings.CategoryUI, "id"); got != "🎨 Antarmuka" {
		t.Fatalf("localized category=%q", got)
	}
}

func viewContainsButton(view presentation.View, label string) bool {
	for _, row := range view.Rows {
		for _, button := range row {
			if button.Text == label {
				return true
			}
		}
	}
	return false
}
