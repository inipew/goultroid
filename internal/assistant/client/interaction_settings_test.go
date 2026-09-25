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
	"github.com/inipew/goultroid/internal/settings"
)

type shellSettingsRepo struct {
	mu             sync.Mutex
	items          map[string]*settings.SettingItem
	effectiveReads int
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
	r.effectiveReads++
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

func (r *shellSettingsRepo) EffectiveReadCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.effectiveReads
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

func TestAssistantShellSettingsNavigationUsesCentralService(t *testing.T) {
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
	if !strings.Contains(port.edited.Text, "GoUltroid Settings") {
		t.Fatalf("settings home not rendered from registry: %q", port.edited.Text)
	}
	if err := dispatchShell(t, engine, settingsFromHome, 401, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("old settings token error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}
	if got := repo.EffectiveReadCount(); got != 0 {
		t.Fatalf("settings root performed effective value reads = %d, want 0", got)
	}

	openGeneral := callbackForAction(t, port.edited, assistantshell.SettingsCategorySlotActionIDs()[0])
	if err := dispatchShell(t, engine, openGeneral, 402, peer); err != nil {
		t.Fatalf("Dispatch(general slot) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "General") {
		t.Fatalf("general category missing: %q", port.edited.Text)
	}
	if got := repo.EffectiveReadCount(); got != 0 {
		t.Fatalf("settings category grid performed effective value reads = %d, want 0", got)
	}

	openPrefix := callbackForAction(t, port.edited, assistantshell.SettingSlotActionIDs()[0])
	if err := dispatchShell(t, engine, openPrefix, 403, peer); err != nil {
		t.Fatalf("Dispatch(prefix slot) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "User override") || !strings.Contains(port.edited.Text, "• <b>Current:</b> !") {
		t.Fatalf("prefix detail missing source/value: %q", port.edited.Text)
	}
	if got := repo.EffectiveReadCount(); got == 0 {
		t.Fatal("setting detail did not resolve effective value")
	}
	if callbackForAction(t, port.edited, assistantshell.ActionSettingInput) == nil {
		t.Fatal("string detail missing a2 free-form input action")
	}
	if callbackForAction(t, port.edited, assistantshell.ActionSettingReset) == nil {
		t.Fatal("explicit user override missing reset action")
	}

	backCategories := callbackForAction(t, port.edited, assistantshell.ActionSettings)
	if err := dispatchShell(t, engine, backCategories, 404, peer); err != nil {
		t.Fatalf("Dispatch(categories) error = %v", err)
	}
	openSecurity := callbackForAction(t, port.edited, assistantshell.SettingsCategorySlotActionIDs()[1])
	if err := dispatchShell(t, engine, openSecurity, 405, peer); err != nil {
		t.Fatalf("Dispatch(security slot) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Security") {
		t.Fatalf("security category not opened: %q", port.edited.Text)
	}

	openToken := callbackForAction(t, port.edited, assistantshell.SettingSlotActionIDs()[0])
	if err := dispatchShell(t, engine, openToken, 406, peer); err != nil {
		t.Fatalf("Dispatch(token slot) error = %v", err)
	}
	if strings.Contains(port.edited.Text, "runtime-secret") || strings.Contains(port.edited.Text, "default-secret") || !strings.Contains(port.edited.Text, "••••") {
		t.Fatalf("sensitive detail leaked value: %q", port.edited.Text)
	}
	if got := manager.InteractionRuntime().Stats().Sessions; got != 1 {
		t.Fatalf("settings navigation sessions = %d, want 1", got)
	}
}

func TestAssistantShellSettingResetRequiresConfirmation(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	if err := registry.Register(settings.SettingDefinition{
		Namespace: "core", Key: "prefix", Type: settings.TypeString, DefaultValue: ".", Title: "Prefix", Category: "general",
	}); err != nil {
		t.Fatal(err)
	}
	repo := newShellSettingsRepo()
	if err := repo.Set(context.Background(), settings.ScopeUser, 7, "core", "prefix", "!"); err != nil {
		t.Fatal(err)
	}
	client.SetSettingsService(settings.NewService(repo, registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	beginShell(t, engine, port, peer)
	if err := dispatchShell(t, engine, callbackForAction(t, port.sent, assistantshell.ActionSettings), 600, peer); err != nil {
		t.Fatal(err)
	}
	if err := dispatchShell(t, engine, callbackForAction(t, port.edited, assistantshell.SettingsCategorySlotActionIDs()[0]), 601, peer); err != nil {
		t.Fatal(err)
	}
	if err := dispatchShell(t, engine, callbackForAction(t, port.edited, assistantshell.SettingSlotActionIDs()[0]), 602, peer); err != nil {
		t.Fatal(err)
	}
	reset := callbackForAction(t, port.edited, assistantshell.ActionSettingReset)
	if err := dispatchShell(t, engine, reset, 603, peer); err != nil {
		t.Fatalf("Dispatch(reset opener) error=%v", err)
	}
	if callbackForAction(t, port.edited, assistantshell.ActionSettingResetConfirm) == nil {
		t.Fatal("reset confirmation action missing")
	}
	value, err := repo.Get(context.Background(), settings.ScopeUser, 7, "core", "prefix")
	if err != nil || value == nil || value.Value != "!" {
		t.Fatalf("reset opener mutated setting: value=%+v err=%v", value, err)
	}
	confirm := callbackForAction(t, port.edited, assistantshell.ActionSettingResetConfirm)
	if err := dispatchShell(t, engine, confirm, 604, peer); err != nil {
		t.Fatalf("Dispatch(reset confirm) error=%v", err)
	}
	value, err = repo.Get(context.Background(), settings.ScopeUser, 7, "core", "prefix")
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		t.Fatalf("confirmed reset retained override: %+v", value)
	}
}

func TestAssistantShellSettingsSlotRejectsRegistryRemap(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	registry := settings.NewRegistry()
	for _, def := range []settings.SettingDefinition{
		{Namespace: "media", Key: "enabled", Type: settings.TypeBool, DefaultValue: "false", Title: "Media", Category: "media"},
		{Namespace: "system", Key: "enabled", Type: settings.TypeBool, DefaultValue: "false", Title: "System", Category: "system"},
	} {
		if err := registry.Register(def); err != nil {
			t.Fatalf("Register(%s) error = %v", def.Namespace, err)
		}
	}
	client.SetSettingsService(settings.NewService(newShellSettingsRepo(), registry, nil))

	peer := &tg.InputPeerUser{UserID: 7}
	beginShell(t, engine, port, peer)
	settingsAction := callbackForAction(t, port.sent, assistantshell.ActionSettings)
	if err := dispatchShell(t, engine, settingsAction, 500, peer); err != nil {
		t.Fatalf("Dispatch(settings) error = %v", err)
	}
	oldSlot := callbackForAction(t, port.edited, assistantshell.SettingsCategorySlotActionIDs()[0])

	if err := registry.Register(settings.SettingDefinition{
		Namespace: "aaa", Key: "enabled", Type: settings.TypeBool, DefaultValue: "false", Title: "AAA", Category: "aardvark",
	}); err != nil {
		t.Fatalf("Register(aardvark) error = %v", err)
	}
	if err := dispatchShell(t, engine, oldSlot, 501, peer); !errors.Is(err, ErrShellSettingsSelectionStale) {
		t.Fatalf("registry-remapped slot error = %v, want %v", err, ErrShellSettingsSelectionStale)
	}
	if !strings.Contains(port.edited.Text, "GoUltroid Settings") {
		t.Fatalf("stale settings slot unexpectedly transitioned view: %q", port.edited.Text)
	}
}
