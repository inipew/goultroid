package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/settings"
)

func TestAssistantShellLanguageSelectorPersistsCanonicalUserSetting(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	if err := settings.RegisterDefaultDefinitions(registry); err != nil {
		t.Fatal(err)
	}
	repo := newShellSettingsRepo()
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	beginShell(t, engine, port, peer)

	openLanguage := callbackForAction(t, port.sent, assistantshell.ActionLanguage)
	if err := dispatchShell(t, engine, openLanguage, 700, peer); err != nil {
		t.Fatalf("Dispatch(language) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "List Of Available Languages.") {
		t.Fatalf("English language selector not rendered: %q", port.edited.Text)
	}

	setIndonesian := callbackForAction(t, port.edited, assistantshell.ActionLanguageIndonesian)
	if err := dispatchShell(t, engine, setIndonesian, 701, peer); err != nil {
		t.Fatalf("Dispatch(language_id) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Daftar Bahasa Tersedia.") ||
		!strings.Contains(port.edited.Text, "Preferensi bahasa tersimpan") {
		t.Fatalf("Indonesian selector not rendered after persistence: %q", port.edited.Text)
	}
	item, err := repo.GetSetting(context.Background(), string(settings.ScopeUser), 7, assistantshell.LocaleSettingNamespace, assistantshell.LocaleSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	if item == nil || item.Value != "id" {
		t.Fatalf("canonical user locale item=%+v", item)
	}
	if err := dispatchShell(t, engine, setIndonesian, 702, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("old locale callback error=%v, want stale token", err)
	}

	home := callbackForAction(t, port.edited, assistantshell.ActionHome)
	if err := dispatchShell(t, engine, home, 703, peer); err != nil {
		t.Fatalf("Dispatch(home) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Silakan telusuri opsi") {
		t.Fatalf("persisted locale did not drive next shell render: %q", port.edited.Text)
	}
}
