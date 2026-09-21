package client

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gotd/td/tg"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/settings"
)

type shellSettingsRepo struct {
	mu    sync.Mutex
	items map[string]*settings.SettingItem
}

func newShellSettingsRepo() *shellSettingsRepo {
	return &shellSettingsRepo{items: make(map[string]*settings.SettingItem)}
}

func (r *shellSettingsRepo) key(scope string, scopeID int64, namespace, key string) string {
	return scope + ":" + namespace + ":" + key + ":" + strconv.FormatInt(scopeID, 10)
}

func (r *shellSettingsRepo) GetSetting(_ context.Context, scope string, scopeID int64, namespace, key string) (*settings.SettingItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.items[r.key(scope, scopeID, namespace, key)]
	if item == nil {
		return nil, nil
	}
	copyItem := *item
	return &copyItem, nil
}

func (r *shellSettingsRepo) GetEffectiveSetting(_ context.Context, namespace, key string, chatID, userID int64) (*settings.SettingItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ref := range []struct {
		scope string
		id    int64
	}{
		{scope: string(settings.ScopeChat), id: chatID},
		{scope: string(settings.ScopeUser), id: userID},
		{scope: string(settings.ScopeGlobal), id: 0},
	} {
		if ref.id == 0 && ref.scope != string(settings.ScopeGlobal) {
			continue
		}
		if item := r.items[r.key(ref.scope, ref.id, namespace, key)]; item != nil {
			copyItem := *item
			return &copyItem, nil
		}
	}
	return nil, nil
}

func (r *shellSettingsRepo) SetSetting(_ context.Context, item *settings.SettingItem) error {
	if item == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	copyItem := *item
	r.items[r.key(item.ScopeType, item.ScopeID, item.Namespace, item.Key)] = &copyItem
	return nil
}

func (r *shellSettingsRepo) SetSettingsBatch(ctx context.Context, items []*settings.SettingItem) error {
	for _, item := range items {
		if err := r.SetSetting(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (r *shellSettingsRepo) DeleteSetting(_ context.Context, scope string, scopeID int64, namespace, key string) error {
	r.mu.Lock()
	delete(r.items, r.key(scope, scopeID, namespace, key))
	r.mu.Unlock()
	return nil
}

func (*shellSettingsRepo) ListSettings(context.Context, string, int64, string) ([]settings.SettingItem, error) {
	return nil, nil
}

func (*shellSettingsRepo) GetSettingHistory(context.Context, string, string, int) ([]settings.SettingChangeRecord, error) {
	return nil, nil
}

func (*shellSettingsRepo) ListPendingOutbox(context.Context, int) ([]settings.SettingOutboxEntry, error) {
	return nil, nil
}

func (*shellSettingsRepo) MarkOutboxProcessed(context.Context, int64) error { return nil }

func TestAssistantShellSettingsReadOnlyNavigationUsesCentralService(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	if err := registry.Register(settings.SettingDefinition{
		Namespace:    "core",
		Key:          "prefix",
		Type:         settings.TypeString,
		DefaultValue: ".",
		Title:        "Command Prefix",
		Description:  "Prefix used by userbot commands.",
		Category:     settings.CategoryGeneral,
		UI:           settings.UIHint{Widget: settings.WidgetText},
	}); err != nil {
		t.Fatalf("Register(prefix) error = %v", err)
	}
	if err := registry.Register(settings.SettingDefinition{
		Namespace:    "security",
		Key:          "token",
		Type:         settings.TypeString,
		DefaultValue: "default-secret",
		Title:        "API Token",
		Description:  "Sensitive token.",
		Category:     settings.CategorySecurity,
		UI:           settings.UIHint{Widget: settings.WidgetText},
		Sensitive:    true,
	}); err != nil {
		t.Fatalf("Register(token) error = %v", err)
	}
	repo := newShellSettingsRepo()
	if err := repo.SetSetting(context.Background(), &settings.SettingItem{
		ScopeType: string(settings.ScopeUser),
		ScopeID:   7,
		Namespace: "core",
		Key:       "prefix",
		ValueType: string(settings.TypeString),
		Value:     "!",
	}); err != nil {
		t.Fatalf("seed prefix error = %v", err)
	}
	if err := repo.SetSetting(context.Background(), &settings.SettingItem{
		ScopeType: string(settings.ScopeUser),
		ScopeID:   7,
		Namespace: "security",
		Key:       "token",
		ValueType: string(settings.TypeString),
		Value:     "runtime-secret",
	}); err != nil {
		t.Fatalf("seed token error = %v", err)
	}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	beginShell(t, engine, port, peer)

	settingsFromHome := callbackForAction(t, port.sent, assistantshell.ActionSettings)
	if err := dispatchShell(t, engine, settingsFromHome, 400, peer); err != nil {
		t.Fatalf("Dispatch(settings) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "GoUltroid Settings") || !strings.Contains(port.edited.Text, "General") {
		t.Fatalf("settings home not rendered from registry: %q", port.edited.Text)
	}
	if err := dispatchShell(t, engine, settingsFromHome, 401, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("old settings token error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}

	openGeneral := callbackForAction(t, port.edited, assistantshell.ActionSettingsOpen)
	if err := dispatchShell(t, engine, openGeneral, 402, peer); err != nil {
		t.Fatalf("Dispatch(open general) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Command Prefix") || !strings.Contains(port.edited.Text, "<code>!</code>") {
		t.Fatalf("general category missing effective value: %q", port.edited.Text)
	}

	openPrefix := callbackForAction(t, port.edited, assistantshell.ActionSettingOpen)
	if err := dispatchShell(t, engine, openPrefix, 403, peer); err != nil {
		t.Fatalf("Dispatch(prefix detail) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "User override") || !strings.Contains(port.edited.Text, "<code>!</code>") {
		t.Fatalf("prefix detail missing source/value: %q", port.edited.Text)
	}
	if strings.Contains(port.edited.Text, "Change") || strings.Contains(port.edited.Text, "Reset") {
		t.Fatalf("read-only settings detail exposed mutation control: %q", port.edited.Text)
	}

	backCategories := callbackForAction(t, port.edited, assistantshell.ActionSettings)
	if err := dispatchShell(t, engine, backCategories, 404, peer); err != nil {
		t.Fatalf("Dispatch(categories) error = %v", err)
	}
	nextCategory := callbackForAction(t, port.edited, assistantshell.ActionSettingsNext)
	if err := dispatchShell(t, engine, nextCategory, 405, peer); err != nil {
		t.Fatalf("Dispatch(next category) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Security") {
		t.Fatalf("security category not selected: %q", port.edited.Text)
	}

	openSecurity := callbackForAction(t, port.edited, assistantshell.ActionSettingsOpen)
	if err := dispatchShell(t, engine, openSecurity, 406, peer); err != nil {
		t.Fatalf("Dispatch(open security) error = %v", err)
	}
	if strings.Contains(port.edited.Text, "runtime-secret") || !strings.Contains(port.edited.Text, "••••") {
		t.Fatalf("sensitive category value leaked: %q", port.edited.Text)
	}

	openToken := callbackForAction(t, port.edited, assistantshell.ActionSettingOpen)
	if err := dispatchShell(t, engine, openToken, 407, peer); err != nil {
		t.Fatalf("Dispatch(token detail) error = %v", err)
	}
	if strings.Contains(port.edited.Text, "runtime-secret") || strings.Contains(port.edited.Text, "default-secret") {
		t.Fatalf("sensitive detail leaked value: %q", port.edited.Text)
	}
	if got := manager.InteractionRuntime().Stats().Sessions; got != 1 {
		t.Fatalf("settings navigation sessions = %d, want 1", got)
	}
}
