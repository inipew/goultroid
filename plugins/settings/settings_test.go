package settings

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/settings"
)

type mockSettingsDB struct {
	database.Repository
	mu      sync.Mutex
	items   map[string]*database.SettingItem
	history []database.SettingChangeRecord
	idGen   int64
}

func newMockSettingsDB() *mockSettingsDB {
	return &mockSettingsDB{
		items: make(map[string]*database.SettingItem),
	}
}

func (m *mockSettingsDB) key(scopeType string, scopeID int64, ns, k string) string {
	return scopeType + ":" + string(rune(scopeID)) + ":" + ns + ":" + k
}

func (m *mockSettingsDB) GetSetting(ctx context.Context, scopeType string, scopeID int64, namespace, key string) (*database.SettingItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[m.key(scopeType, scopeID, namespace, key)]
	if !ok {
		return nil, nil
	}
	cp := *item
	return &cp, nil
}

func (m *mockSettingsDB) SetSetting(ctx context.Context, item *database.SettingItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(item.ScopeType, item.ScopeID, item.Namespace, item.Key)
	oldVal := ""
	if prev, ok := m.items[k]; ok {
		oldVal = prev.Value
	}
	cp := *item
	m.items[k] = &cp
	m.idGen++
	m.history = append([]database.SettingChangeRecord{{
		ID:        m.idGen,
		ScopeType: item.ScopeType,
		ScopeID:   item.ScopeID,
		Namespace: item.Namespace,
		Key:       item.Key,
		OldVal:    oldVal,
		NewVal:    item.Value,
		ChangedBy: item.UpdatedBy,
		ChangedAt: time.Now().UTC(),
	}}, m.history...)
	return nil
}

func (m *mockSettingsDB) DeleteSetting(ctx context.Context, scopeType string, scopeID int64, namespace, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(scopeType, scopeID, namespace, key)
	if prev, ok := m.items[k]; ok {
		delete(m.items, k)
		m.idGen++
		m.history = append([]database.SettingChangeRecord{{
			ID:        m.idGen,
			ScopeType: scopeType,
			ScopeID:   scopeID,
			Namespace: namespace,
			Key:       key,
			OldVal:    prev.Value,
			NewVal:    "",
			ChangedBy: 0,
			ChangedAt: time.Now().UTC(),
		}}, m.history...)
	}
	return nil
}

func (m *mockSettingsDB) ListSettings(ctx context.Context, scopeType string, scopeID int64, namespace string) ([]database.SettingItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var res []database.SettingItem
	for _, item := range m.items {
		if item.ScopeType == scopeType && item.ScopeID == scopeID {
			if namespace == "" || item.Namespace == namespace {
				res = append(res, *item)
			}
		}
	}
	return res, nil
}

func (m *mockSettingsDB) GetSettingHistory(ctx context.Context, namespace, key string, limit int) ([]database.SettingChangeRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var res []database.SettingChangeRecord
	for _, r := range m.history {
		if r.Namespace == namespace && r.Key == key {
			res = append(res, r)
		}
	}
	return res, nil
}

// mockTelegramService records responses for assertion
type mockTelegramService struct {
	core.MockTelegramServicer
	mu            sync.Mutex
	lastText      string
	lastMarkup    tg.ReplyMarkupClass
	answeredText  string
	answeredAlert bool
}

func (m *mockTelegramService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastText = text
	m.lastMarkup = nil
	return &tg.Message{ID: 100, Message: text}, nil
}

func (m *mockTelegramService) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastText = text
	m.lastMarkup = markup
	return &tg.Message{ID: 100, Message: text}, nil
}

func (m *mockTelegramService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastText = text
	m.lastMarkup = nil
	return nil
}

func (m *mockTelegramService) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastText = text
	m.lastMarkup = markup
	return nil
}

func (m *mockTelegramService) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answeredText = text
	m.answeredAlert = alert
	return nil
}

func setupTestPlugin(t *testing.T) (*Plugin, *settings.Service, *callback.StateStore, *mockTelegramService) {
	db := newMockSettingsDB()
	reg := settings.NewRegistry()
	_ = settings.RegisterDefaultDefinitions(reg)
	bus := core.NewEventBus()
	svc := settings.NewService(db, reg, bus)
	store := callback.NewStateStore()
	plugin := New(svc, store)
	tgSvc := &mockTelegramService{}
	return plugin, svc, store, tgSvc
}

func TestPlugin_CommandsRegistration(t *testing.T) {
	p, _, _, _ := setupTestPlugin(t)

	if p.Name() != "settings" {
		t.Errorf("expected 'settings', got %s", p.Name())
	}
	if p.Namespace() != "settings" {
		t.Errorf("expected 'settings', got %s", p.Namespace())
	}

	cmds := p.Commands()
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}
	if cmds[0].Name != "settings" || cmds[1].Name != "config" {
		t.Errorf("unexpected commands: %+v", cmds)
	}
}

func TestPlugin_DashboardRender(t *testing.T) {
	p, _, _, tgSvc := setupTestPlugin(t)

	ctx := &core.Context{
		Ctx:     context.Background(),
		Message: &core.Message{ID: 1, SenderID: 12345},
		Sender:  &core.User{ID: 12345},
		Chat:    &core.Chat{ID: -100123},
		Svc:     tgSvc,
		PeerID:  &tg.InputPeerChat{ChatID: 123},
		Args:    []string{},
	}

	// 1. Open dashboard (home)
	if err := p.handleSettingsCommand(ctx); err != nil {
		t.Fatalf("handleSettingsCommand failed: %v", err)
	}

	tgSvc.mu.Lock()
	text := tgSvc.lastText
	markup := tgSvc.lastMarkup
	tgSvc.mu.Unlock()

	if !strings.Contains(text, "GoUltroid Settings Dashboard") {
		t.Errorf("expected dashboard title in text: %s", text)
	}
	if markup == nil {
		t.Fatal("expected reply markup for settings dashboard")
	}

	// 2. Open specific category via command
	ctx.Args = []string{"security"}
	if err := p.handleSettingsCommand(ctx); err != nil {
		t.Fatalf("handleSettingsCommand with category failed: %v", err)
	}

	tgSvc.mu.Lock()
	textCat := tgSvc.lastText
	tgSvc.mu.Unlock()

	if !strings.Contains(textCat, "Security") {
		t.Errorf("expected Security category text, got: %s", textCat)
	}
}

func TestPlugin_CLIConfig(t *testing.T) {
	p, _, _, tgSvc := setupTestPlugin(t)

	ctx := &core.Context{
		Ctx:     context.Background(),
		Message: &core.Message{ID: 1, SenderID: 12345},
		Sender:  &core.User{ID: 12345},
		Chat:    &core.Chat{ID: -100123},
		Svc:     tgSvc,
		PeerID:  &tg.InputPeerChat{ChatID: 123},
	}

	// 1. Usage
	ctx.Args = []string{}
	_ = p.handleConfigCommand(ctx)
	if !strings.Contains(tgSvc.lastText, "GoUltroid CLI Configuration Subsystem") {
		t.Errorf("expected config usage text, got: %s", tgSvc.lastText)
	}

	// 2. Get default prefix
	ctx.Args = []string{"get", "core:prefix"}
	_ = p.handleConfigCommand(ctx)
	if !strings.Contains(tgSvc.lastText, "**core:prefix** = `.`") {
		t.Errorf("expected '.' prefix, got: %s", tgSvc.lastText)
	}

	// 3. Set prefix to "!"
	ctx.Args = []string{"set", "core:prefix", "!"}
	_ = p.handleConfigCommand(ctx)
	if !strings.Contains(tgSvc.lastText, "Setting updated") {
		t.Errorf("expected setting updated, got: %s", tgSvc.lastText)
	}

	// Verify get returns "!"
	ctx.Args = []string{"get", "core:prefix"}
	_ = p.handleConfigCommand(ctx)
	if !strings.Contains(tgSvc.lastText, "**core:prefix** = `!`") {
		t.Errorf("expected '!' prefix, got: %s", tgSvc.lastText)
	}

	// 4. History
	ctx.Args = []string{"history", "core:prefix"}
	_ = p.handleConfigCommand(ctx)
	if !strings.Contains(tgSvc.lastText, "Change History") {
		t.Errorf("expected history output, got: %s", tgSvc.lastText)
	}

	// 5. Reset
	ctx.Args = []string{"reset", "core:prefix"}
	_ = p.handleConfigCommand(ctx)
	if !strings.Contains(tgSvc.lastText, "reset to default") {
		t.Errorf("expected reset output, got: %s", tgSvc.lastText)
	}

	// 6. Export
	ctx.Args = []string{"export"}
	_ = p.handleConfigCommand(ctx)
	if !strings.Contains(tgSvc.lastText, "Global Settings Export") {
		t.Errorf("expected export output, got: %s", tgSvc.lastText)
	}

	// 7. List
	ctx.Args = []string{"list", "general"}
	_ = p.handleConfigCommand(ctx)
	if !strings.Contains(tgSvc.lastText, "core:prefix") {
		t.Errorf("expected list to contain core:prefix, got: %s", tgSvc.lastText)
	}
}

func TestPlugin_InteractiveCallbacks(t *testing.T) {
	p, svc, store, tgSvc := setupTestPlugin(t)
	ctx := context.Background()

	// Initial check: pmpermit:enabled is true
	val, _ := svc.ResolveBool(ctx, 12345, 0, "pmpermit", "enabled")
	if !val {
		t.Fatal("expected default pmpermit:enabled true")
	}

	// 1. Toggle pmpermit:enabled
	st := MenuState{
		Scope:    settings.ScopeGlobal,
		ScopeID:  0,
		Category: "security",
		Selected: "pmpermit:enabled",
		OwnerID:  12345,
	}
	oid := store.Store(st, 12345, time.Minute)

	cbCtx := &callback.CallbackContext{
		Ctx:      ctx,
		QueryID:  111,
		UserID:   12345,
		Action:   "toggle",
		OpaqueID: oid,
		State:    st,
		Service:  tgSvc,
		Target:   core.CallbackTarget{Peer: &tg.InputPeerUser{UserID: 12345}, MessageID: 1},
	}

	if err := p.HandleCallback(cbCtx); err != nil {
		t.Fatalf("HandleCallback toggle failed: %v", err)
	}

	// Verify toggled to false
	valAfter, _ := svc.ResolveBool(ctx, 12345, 0, "pmpermit", "enabled")
	if valAfter {
		t.Error("expected pmpermit:enabled to be toggled to false")
	}

	// 2. Select enum: antispam:action -> mute
	stSelect := MenuState{
		Scope:    settings.ScopeGlobal,
		ScopeID:  0,
		Category: "moderation",
		Selected: "antispam:action:mute",
		OwnerID:  12345,
	}
	oidSelect := store.Store(stSelect, 12345, time.Minute)
	cbCtxSelect := &callback.CallbackContext{
		Ctx:      ctx,
		QueryID:  222,
		UserID:   12345,
		Action:   "select",
		OpaqueID: oidSelect,
		State:    stSelect,
		Service:  tgSvc,
		Target:   core.CallbackTarget{Peer: &tg.InputPeerUser{UserID: 12345}, MessageID: 1},
	}
	if err := p.HandleCallback(cbCtxSelect); err != nil {
		t.Fatalf("HandleCallback select failed: %v", err)
	}

	valAction, _ := svc.Resolve(ctx, 12345, 0, "antispam", "action")
	if valAction != "mute" {
		t.Errorf("expected 'mute', got %s", valAction)
	}

	// 3. Step int: pmpermit:max_warns -> 5
	stStep := MenuState{
		Scope:    settings.ScopeGlobal,
		ScopeID:  0,
		Category: "security",
		Selected: "pmpermit:max_warns:5",
		OwnerID:  12345,
	}
	oidStep := store.Store(stStep, 12345, time.Minute)
	cbCtxStep := &callback.CallbackContext{
		Ctx:      ctx,
		QueryID:  333,
		UserID:   12345,
		Action:   "step",
		OpaqueID: oidStep,
		State:    stStep,
		Service:  tgSvc,
		Target:   core.CallbackTarget{Peer: &tg.InputPeerUser{UserID: 12345}, MessageID: 1},
	}
	if err := p.HandleCallback(cbCtxStep); err != nil {
		t.Fatalf("HandleCallback step failed: %v", err)
	}

	valWarns, _ := svc.ResolveInt(ctx, 12345, 0, "pmpermit", "max_warns")
	if valWarns != 5 {
		t.Errorf("expected 5 warns, got %d", valWarns)
	}

	// 4. Duration picker preset callback: afk:cooldown -> 15m0s
	stDur := MenuState{
		Scope:    settings.ScopeGlobal,
		ScopeID:  0,
		Category: "automation",
		Selected: "afk:cooldown:15m0s",
		OwnerID:  12345,
	}
	oidDur := store.Store(stDur, 12345, time.Minute)
	cbCtxDur := &callback.CallbackContext{
		Ctx:      ctx,
		QueryID:  444,
		UserID:   12345,
		Action:   "dur",
		OpaqueID: oidDur,
		State:    stDur,
		Service:  tgSvc,
		Target:   core.CallbackTarget{Peer: &tg.InputPeerUser{UserID: 12345}, MessageID: 1},
	}
	if err := p.HandleCallback(cbCtxDur); err != nil {
		t.Fatalf("HandleCallback dur failed: %v", err)
	}
	valDur, _ := svc.ResolveDuration(ctx, 12345, 0, "afk", "cooldown")
	if valDur != 15*time.Minute {
		t.Errorf("expected 15m cooldown, got %v", valDur)
	}

	// 5. Test Inheritance Badging in renderSettingDetailScreen
	stDetail := MenuState{
		Scope:    settings.ScopeChat,
		ScopeID:  -100123,
		Category: "automation",
		Selected: "afk:cooldown",
		OwnerID:  12345,
	}
	// Currently afk:cooldown is set at global scope (15m0s) -> should show "Inherited from Global"
	scr := p.renderSettingDetailScreen(ctx, stDetail)
	textDetail, _ := scr.Render()
	if !strings.Contains(textDetail, "Inherited from Global") {
		t.Errorf("expected 'Inherited from Global' in detail text, got: %s", textDetail)
	}

	// Override at chat scope -> should show "Chat Override"
	_ = svc.Set(ctx, settings.ScopeChat, -100123, "afk", "cooldown", "30s", 12345)
	scrOverridden := p.renderSettingDetailScreen(ctx, stDetail)
	textOverridden, _ := scrOverridden.Render()
	if !strings.Contains(textOverridden, "Chat Override") {
		t.Errorf("expected 'Chat Override' in detail text, got: %s", textOverridden)
	}

	// 6. Close dashboard
	cbCtxClose := &callback.CallbackContext{
		Ctx:     ctx,
		QueryID: 555,
		UserID:  12345,
		Action:  "close",
		Service: tgSvc,
		Target:  core.CallbackTarget{Peer: &tg.InputPeerUser{UserID: 12345}, MessageID: 1},
	}
	if err := p.HandleCallback(cbCtxClose); err != nil {
		t.Fatalf("HandleCallback close failed: %v", err)
	}
	if !strings.Contains(tgSvc.lastText, "dashboard closed") {
		t.Errorf("expected dashboard closed text, got: %s", tgSvc.lastText)
	}
}
