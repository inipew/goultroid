package settings

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/settings"
)

type mockSettingsDB struct {
	mu      sync.Mutex
	items   map[string]*settings.SettingItem
	history []settings.SettingChangeRecord
	idGen   int64
}

func newMockSettingsDB() *mockSettingsDB {
	return &mockSettingsDB{
		items: make(map[string]*settings.SettingItem),
	}
}

func (m *mockSettingsDB) key(scopeType string, scopeID int64, ns, k string) string {
	return scopeType + ":" + string(rune(scopeID)) + ":" + ns + ":" + k
}

func (m *mockSettingsDB) GetSetting(ctx context.Context, scopeType string, scopeID int64, namespace, key string) (*settings.SettingItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[m.key(scopeType, scopeID, namespace, key)]
	if !ok {
		return nil, nil
	}
	cp := *item
	return &cp, nil
}

func (m *mockSettingsDB) GetEffectiveSetting(ctx context.Context, namespace, key string, chatID, userID int64) (*settings.SettingItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if chatID != 0 {
		if item, ok := m.items[m.key("chat", chatID, namespace, key)]; ok {
			cp := *item
			return &cp, nil
		}
	}
	if userID != 0 {
		if item, ok := m.items[m.key("user", userID, namespace, key)]; ok {
			cp := *item
			return &cp, nil
		}
	}
	if item, ok := m.items[m.key("global", 0, namespace, key)]; ok {
		cp := *item
		return &cp, nil
	}
	return nil, nil
}

func (m *mockSettingsDB) ListPendingOutbox(ctx context.Context, limit int) ([]settings.SettingOutboxEntry, error) {
	return nil, nil
}

func (m *mockSettingsDB) MarkOutboxProcessed(ctx context.Context, id int64) error {
	return nil
}

func (m *mockSettingsDB) SetSetting(ctx context.Context, item *settings.SettingItem) error {
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
	m.history = append([]settings.SettingChangeRecord{{
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

func (m *mockSettingsDB) SetSettingsBatch(ctx context.Context, items []*settings.SettingItem) error {
	for _, item := range items {
		if err := m.SetSetting(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (m *mockSettingsDB) DeleteSetting(ctx context.Context, scopeType string, scopeID int64, namespace, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(scopeType, scopeID, namespace, key)
	if prev, ok := m.items[k]; ok {
		delete(m.items, k)
		m.idGen++
		m.history = append([]settings.SettingChangeRecord{{
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

func (m *mockSettingsDB) ListSettings(ctx context.Context, scopeType string, scopeID int64, namespace string) ([]settings.SettingItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var res []settings.SettingItem
	for _, item := range m.items {
		if item.ScopeType == scopeType && item.ScopeID == scopeID {
			if namespace == "" || item.Namespace == namespace {
				res = append(res, *item)
			}
		}
	}
	return res, nil
}

func (m *mockSettingsDB) GetSettingHistory(ctx context.Context, namespace, key string, limit int) ([]settings.SettingChangeRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var res []settings.SettingChangeRecord
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
	sendCalls     int
	editCalls     int
}

func (m *mockTelegramService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastText = text
	m.lastMarkup = nil
	m.sendCalls++
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
	m.editCalls++
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

func setupTestPlugin(t *testing.T) (*Plugin, *settings.Service, *mockTelegramService) {
	db := newMockSettingsDB()
	reg := settings.NewRegistry()
	_ = settings.RegisterDefaultDefinitions(reg)
	bus := core.NewEventBus()
	svc := settings.NewService(db, reg, bus)
	plugin := New(svc)
	tgSvc := &mockTelegramService{}
	return plugin, svc, tgSvc
}

func TestPlugin_CommandsRegistration(t *testing.T) {
	p, _, _ := setupTestPlugin(t)

	if p.Name() != "settings" {
		t.Errorf("expected 'settings', got %s", p.Name())
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
	p, svc, tgSvc := setupTestPlugin(t)
	if err := svc.Set(context.Background(), settings.ScopeGlobal, 0, "ui", "inline_buttons", "false", 12345); err != nil {
		t.Fatal(err)
	}

	ctx := &core.Context{
		Ctx:     context.Background(),
		Source:  core.ExecutionAssistant,
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
	if markup != nil {
		t.Fatal("text-only settings dashboard unexpectedly returned reply markup")
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
	p, _, tgSvc := setupTestPlugin(t)

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
	if !strings.Contains(tgSvc.lastText, "GoUltroid CLI Configuration Subsystem") || !strings.Contains(tgSvc.lastText, "&lt;namespace:key&gt;") {
		t.Errorf("expected config usage text with arguments, got: %s", tgSvc.lastText)
	}

	// 2. Get default prefix
	ctx.Args = []string{"get", "core:prefix"}
	_ = p.handleConfigCommand(ctx)
	if !strings.Contains(tgSvc.lastText, "<b>core:prefix</b> = <code>.</code>") {
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
	if !strings.Contains(tgSvc.lastText, "<b>core:prefix</b> = <code>!</code>") {
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

func TestPlugin_CLIConfigOutgoingUsesSemanticEdit(t *testing.T) {
	p, _, tgSvc := setupTestPlugin(t)
	ctx := &core.Context{
		Ctx:     context.Background(),
		Message: &core.Message{ID: 77, SenderID: 12345, IsOutgoing: true},
		Sender:  &core.User{ID: 12345},
		Chat:    &core.Chat{ID: -100123},
		Svc:     tgSvc,
		PeerID:  &tg.InputPeerChat{ChatID: 123},
		Args:    []string{"get", "core:prefix"},
	}

	if err := p.handleConfigCommand(ctx); err != nil {
		t.Fatalf("handleConfigCommand() error = %v", err)
	}
	if tgSvc.sendCalls != 0 || tgSvc.editCalls != 1 {
		t.Fatalf("semantic config transport send/edit=%d/%d, want 0/1", tgSvc.sendCalls, tgSvc.editCalls)
	}
	if ctx.LastResponseID != 77 {
		t.Fatalf("LastResponseID=%d, want outgoing anchor 77", ctx.LastResponseID)
	}
}

func TestPlugin_SettingMutationBoundary(t *testing.T) {
	p, svc, _ := setupTestPlugin(t)
	ctx := context.Background()

	state := MenuState{
		Scope:    settings.ScopeGlobal,
		ScopeID:  0,
		Category: "security",
		Selected: "pmpermit:enabled",
		OwnerID:  12345,
	}
	if err := p.applySettingMutation(ctx, 12345, &state, nativeIntentToggle); err != nil {
		t.Fatalf("toggle mutation failed: %v", err)
	}
	value, err := svc.ResolveBool(ctx, 12345, 0, "pmpermit", "enabled")
	if err != nil {
		t.Fatal(err)
	}
	if value {
		t.Fatal("toggle mutation did not disable pmpermit:enabled")
	}

	state.SetTarget("pmpermit", "max_warns")
	state.ActionValue = "5"
	if err := p.applySettingMutation(ctx, 12345, &state, nativeIntentSet); err != nil {
		t.Fatalf("set mutation failed: %v", err)
	}
	warns, err := svc.ResolveInt(ctx, 12345, 0, "pmpermit", "max_warns")
	if err != nil {
		t.Fatal(err)
	}
	if warns != 5 {
		t.Fatalf("max_warns=%d, want 5", warns)
	}

	if err := p.applySettingMutation(ctx, 12345, &state, nativeIntentReset); err != nil {
		t.Fatalf("reset mutation failed: %v", err)
	}
}

func TestPlugin_ScopeSwitchingAndTarget(t *testing.T) {
	st := MenuState{
		Scope:   settings.ScopeGlobal,
		ScopeID: 0,
		ChatID:  -100777,
		OwnerID: 888,
	}

	st.SetTarget("core", "prefix")
	ns, key := st.GetTarget()
	if ns != "core" || key != "prefix" {
		t.Errorf("expected core:prefix, got %s:%s", ns, key)
	}
	if st.Selected != "core:prefix" {
		t.Errorf("expected Selected synced to core:prefix, got %s", st.Selected)
	}

	// Verify backward compat when only Selected is set (e.g. from older serialized states)
	stOld := MenuState{Selected: "afk:cooldown"}
	nsOld, keyOld := stOld.GetTarget()
	if nsOld != "afk" || keyOld != "cooldown" {
		t.Errorf("expected afk:cooldown, got %s:%s", nsOld, keyOld)
	}

	// Verify scope switching bindings.
	p, svc, _ := setupTestPlugin(t)
	ctx := context.Background()

	// Text-only render helpers intentionally carry no callback rows after P1-F2.
	scr := p.renderHomeScreen(ctx, st)
	_, markup := scr.Render()
	if len(markup.Rows) != 0 {
		t.Fatalf("text-only home retained %d callback rows", len(markup.Rows))
	}

	// Test transport-neutral mutation boundary with typed Target.
	stAction := MenuState{
		Scope:       settings.ScopeChat,
		ScopeID:     -100777,
		Target:      &SettingTarget{Namespace: "core", Key: "prefix"},
		ActionValue: "!",
		OwnerID:     888,
		ChatID:      -100777,
	}
	if err := p.applySettingMutation(ctx, 888, &stAction, nativeIntentSet); err != nil {
		t.Fatalf("applySettingMutation set failed: %v", err)
	}
	val, err := svc.Resolve(ctx, 888, -100777, "core", "prefix")
	if err != nil || val != "!" {
		t.Errorf("expected '!' set for chat scope, got: %q (err=%v)", val, err)
	}

	// 3. Reset through the same canonical mutation boundary.
	if err := p.applySettingMutation(ctx, 888, &stAction, nativeIntentReset); err != nil {
		t.Fatalf("applySettingMutation reset failed: %v", err)
	}
	valReset, _ := svc.Resolve(ctx, 888, -100777, "core", "prefix")
	if valReset != "." {
		t.Errorf("expected fallback to default '.' after reset, got: %q", valReset)
	}
}
