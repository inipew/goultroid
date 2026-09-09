package settings

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

// mockRepo is an in-memory database.Repository stub for settings tests.
type mockRepo struct {
	database.Repository
	mu       sync.Mutex
	settings map[string]*database.SettingItem
	history  []database.SettingChangeRecord
	idGen    int64
}

func newMockRepo() *mockRepo {
	return &mockRepo{
		settings: make(map[string]*database.SettingItem),
	}
}

func (m *mockRepo) key(scopeType string, scopeID int64, ns, k string) string {
	return scopeType + ":" + string(rune(scopeID)) + ":" + ns + ":" + k
}

func (m *mockRepo) GetSetting(ctx context.Context, scopeType string, scopeID int64, namespace, key string) (*database.SettingItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.settings[m.key(scopeType, scopeID, namespace, key)]
	if !ok {
		return nil, nil
	}
	cp := *item
	return &cp, nil
}

func (m *mockRepo) GetEffectiveSetting(ctx context.Context, namespace, key string, chatID, userID int64) (*database.SettingItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if chatID != 0 {
		if item, ok := m.settings[m.key("chat", chatID, namespace, key)]; ok {
			cp := *item
			return &cp, nil
		}
	}
	if userID != 0 {
		if item, ok := m.settings[m.key("user", userID, namespace, key)]; ok {
			cp := *item
			return &cp, nil
		}
	}
	if item, ok := m.settings[m.key("global", 0, namespace, key)]; ok {
		cp := *item
		return &cp, nil
	}
	return nil, nil
}

func (m *mockRepo) ListPendingOutbox(ctx context.Context, limit int) ([]database.SettingOutboxEntry, error) {
	return nil, nil
}

func (m *mockRepo) MarkOutboxProcessed(ctx context.Context, id int64) error {
	return nil
}

func (m *mockRepo) SetSetting(ctx context.Context, item *database.SettingItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(item.ScopeType, item.ScopeID, item.Namespace, item.Key)
	oldVal := ""
	if prev, ok := m.settings[k]; ok {
		oldVal = prev.Value
	}
	cp := *item
	m.settings[k] = &cp

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

func (m *mockRepo) SetSettingsBatch(ctx context.Context, items []*database.SettingItem) error {
	for _, item := range items {
		if err := m.SetSetting(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (m *mockRepo) DeleteSetting(ctx context.Context, scopeType string, scopeID int64, namespace, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(scopeType, scopeID, namespace, key)
	oldVal := ""
	if prev, ok := m.settings[k]; ok {
		oldVal = prev.Value
		delete(m.settings, k)
		m.idGen++
		m.history = append([]database.SettingChangeRecord{{
			ID:        m.idGen,
			ScopeType: scopeType,
			ScopeID:   scopeID,
			Namespace: namespace,
			Key:       key,
			OldVal:    oldVal,
			NewVal:    "",
			ChangedBy: 0,
			ChangedAt: time.Now().UTC(),
		}}, m.history...)
	}
	return nil
}

func (m *mockRepo) ListSettings(ctx context.Context, scopeType string, scopeID int64, namespace string) ([]database.SettingItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var res []database.SettingItem
	for _, item := range m.settings {
		if item.ScopeType == scopeType && item.ScopeID == scopeID {
			if namespace == "" || item.Namespace == namespace {
				res = append(res, *item)
			}
		}
	}
	return res, nil
}

func (m *mockRepo) GetSettingHistory(ctx context.Context, namespace, key string, limit int) ([]database.SettingChangeRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var res []database.SettingChangeRecord
	for _, r := range m.history {
		if r.Namespace == namespace && r.Key == key {
			res = append(res, r)
			if limit > 0 && len(res) >= limit {
				break
			}
		}
	}
	return res, nil
}

func TestSettingTypesAndValidation(t *testing.T) {
	minVal := int64(10)
	maxVal := int64(100)

	intDef := SettingDefinition{
		Namespace: "test",
		Key:       "number",
		Type:      TypeInt,
		MinVal:    &minVal,
		MaxVal:    &maxVal,
	}

	if err := intDef.Validate("50"); err != nil {
		t.Errorf("expected 50 to be valid, got %v", err)
	}
	if err := intDef.Validate("5"); err == nil {
		t.Error("expected 5 to fail (below min 10)")
	}
	if err := intDef.Validate("150"); err == nil {
		t.Error("expected 150 to fail (above max 100)")
	}
	if err := intDef.Validate("abc"); err == nil {
		t.Error("expected 'abc' to fail int parse")
	}

	boolDef := SettingDefinition{
		Namespace: "test",
		Key:       "flag",
		Type:      TypeBool,
	}
	for _, v := range []string{"true", "false", "1", "0", "yes", "no", "on", "off"} {
		if err := boolDef.Validate(v); err != nil {
			t.Errorf("expected %q to be valid bool: %v", v, err)
		}
	}
	if err := boolDef.Validate("maybe"); err == nil {
		t.Error("expected 'maybe' to fail bool parse")
	}

	cVal, _ := boolDef.Canonicalize("on")
	if cVal != "true" {
		t.Errorf("expected 'true', got %s", cVal)
	}
	cVal, _ = boolDef.Canonicalize("off")
	if cVal != "false" {
		t.Errorf("expected 'false', got %s", cVal)
	}

	durDef := SettingDefinition{
		Namespace: "test",
		Key:       "delay",
		Type:      TypeDuration,
		MinVal:    &minVal,
	}
	if err := durDef.Validate("15s"); err != nil {
		t.Errorf("expected 15s to be valid: %v", err)
	}
	if err := durDef.Validate("2s"); err == nil {
		t.Error("expected 2s to fail min 10s")
	}
	if err := durDef.Validate("invalid"); err == nil {
		t.Error("expected invalid duration to fail")
	}

	enumDef := SettingDefinition{
		Namespace:     "test",
		Key:           "mode",
		Type:          TypeEnum,
		AllowedValues: []string{"fast", "slow", "turbo"},
	}
	if err := enumDef.Validate("fast"); err != nil {
		t.Errorf("expected 'fast' to be valid: %v", err)
	}
	if err := enumDef.Validate("TURBO"); err != nil {
		t.Errorf("expected case-insensitive 'TURBO' to be valid: %v", err)
	}
	if err := enumDef.Validate("ludicrous"); err == nil {
		t.Error("expected 'ludicrous' to fail enum check")
	}

	customDef := SettingDefinition{
		Namespace: "test",
		Key:       "custom",
		Type:      TypeString,
		Validator: func(val string) error {
			if len(val) < 3 {
				return errors.New("too short")
			}
			return nil
		},
	}
	if err := customDef.Validate("ab"); err == nil {
		t.Error("expected custom validator to reject short string")
	}
	if err := customDef.Validate("hello"); err != nil {
		t.Errorf("expected 'hello' to pass validator: %v", err)
	}

	// Test Scope Normalization
	s, err := NormalizeScope("global")
	if err != nil || s != ScopeGlobal {
		t.Errorf("expected global, got %s, err=%v", s, err)
	}
	s, err = NormalizeScope("C")
	if err != nil || s != ScopeChat {
		t.Errorf("expected chat, got %s, err=%v", s, err)
	}
	s, err = NormalizeScope("user")
	if err != nil || s != ScopeUser {
		t.Errorf("expected user, got %s, err=%v", s, err)
	}
	_, err = NormalizeScope("unknown")
	if err == nil {
		t.Error("expected unknown scope to return error")
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()
	if err := RegisterDefaultDefinitions(reg); err != nil {
		t.Fatalf("failed to register default definitions: %v", err)
	}

	def, ok := reg.Get("core", "prefix")
	if !ok || def == nil {
		t.Fatal("expected core:prefix in registry")
	}
	if def.DefaultValue != "." {
		t.Errorf("expected default prefix '.', got %s", def.DefaultValue)
	}

	// Categories
	cats := reg.Categories()
	if len(cats) < 6 {
		t.Fatalf("expected at least 6 categories, got %d", len(cats))
	}

	// ListByCategory
	generalDefs := reg.ListByCategory(CategoryGeneral)
	if len(generalDefs) == 0 {
		t.Error("expected definitions in general category")
	}

	// ListByNamespace
	afkDefs := reg.ListByNamespace("afk")
	if len(afkDefs) < 2 {
		t.Errorf("expected at least 2 afk definitions, got %d", len(afkDefs))
	}

	// ListAll
	allDefs := reg.ListAll()
	if len(allDefs) < 10 {
		t.Errorf("expected at least 10 definitions, got %d", len(allDefs))
	}
}

func TestServiceInheritanceAndOperations(t *testing.T) {
	repo := newMockRepo()
	reg := NewRegistry()
	_ = RegisterDefaultDefinitions(reg)

	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatalf("start event bus: %v", err)
	}
	defer func() { _ = bus.Close() }()
	svc := NewService(repo, reg, bus)
	ctx := context.Background()

	// Capture SettingChangedEvents
	var eventsReceived []core.Event
	var evMu sync.Mutex
	bus.Subscribe(core.EventTypeSettingChanged, func(ev core.Event) {
		evMu.Lock()
		defer evMu.Unlock()
		eventsReceived = append(eventsReceived, ev)
	})

	userID := int64(111)
	chatID := int64(222)

	// 1. Initial resolution: should return schema default "."
	val, err := svc.Resolve(ctx, userID, chatID, "core", "prefix")
	if err != nil || val != "." {
		t.Fatalf("expected default '.', got %q (err=%v)", val, err)
	}

	// 2. Set Global override "!"
	if err := svc.Set(ctx, ScopeGlobal, 0, "core", "prefix", "!", 999); err != nil {
		t.Fatalf("failed to set global prefix: %v", err)
	}
	val, _ = svc.Resolve(ctx, userID, chatID, "core", "prefix")
	if val != "!" {
		t.Errorf("expected global override '!', got %q", val)
	}

	// 3. Set User override "?"
	if err := svc.Set(ctx, ScopeUser, userID, "core", "prefix", "?", userID); err != nil {
		t.Fatalf("failed to set user prefix: %v", err)
	}
	val, _ = svc.Resolve(ctx, userID, chatID, "core", "prefix")
	if val != "?" {
		t.Errorf("expected user override '?', got %q", val)
	}

	// 4. Set Chat override "#" -> highest precedence!
	if err := svc.Set(ctx, ScopeChat, chatID, "core", "prefix", "#", userID); err != nil {
		t.Fatalf("failed to set chat prefix: %v", err)
	}
	val, _ = svc.Resolve(ctx, userID, chatID, "core", "prefix")
	if val != "#" {
		t.Errorf("expected chat override '#', got %q", val)
	}

	// 5. Another chat (chatID 333) should see User override "?"
	valOtherChat, _ := svc.Resolve(ctx, userID, 333, "core", "prefix")
	if valOtherChat != "?" {
		t.Errorf("expected user override '?' for other chat, got %q", valOtherChat)
	}

	// 6. Another user (userID 444) in chat 333 should see Global override "!"
	valOtherUser, _ := svc.Resolve(ctx, 444, 333, "core", "prefix")
	if valOtherUser != "!" {
		t.Errorf("expected global override '!' for other user, got %q", valOtherUser)
	}

	// 7. Reset chat override -> should fall back to user override "?"
	if err := svc.Reset(ctx, ScopeChat, chatID, "core", "prefix", userID); err != nil {
		t.Fatalf("failed to reset chat override: %v", err)
	}
	val, _ = svc.Resolve(ctx, userID, chatID, "core", "prefix")
	if val != "?" {
		t.Errorf("expected fallback to user override '?', got %q", val)
	}

	// 8. Typed resolvers
	bVal, err := svc.ResolveBool(ctx, userID, chatID, "pmpermit", "enabled")
	if err != nil || !bVal {
		t.Errorf("expected default true for pmpermit:enabled, got %v (err=%v)", bVal, err)
	}

	iVal, err := svc.ResolveInt(ctx, userID, chatID, "pmpermit", "max_warns")
	if err != nil || iVal != 3 {
		t.Errorf("expected default 3 for pmpermit:max_warns, got %d (err=%v)", iVal, err)
	}

	dVal, err := svc.ResolveDuration(ctx, userID, chatID, "afk", "cooldown")
	if err != nil || dVal != 5*time.Second {
		t.Errorf("expected default 5s for afk:cooldown, got %v (err=%v)", dVal, err)
	}

	// 9. Validation enforcement on Set
	err = svc.Set(ctx, ScopeGlobal, 0, "pmpermit", "max_warns", "999", userID)
	if err == nil {
		t.Error("expected Set to reject 999 for max_warns (exceeds max 20)")
	}

	// 10. Export and Import
	exportData, err := svc.Export(ctx, ScopeGlobal, 0)
	if err != nil {
		t.Fatalf("failed to export: %v", err)
	}
	if exportData["core"]["prefix"] != "!" {
		t.Errorf("expected global prefix '!' in export, got %v", exportData)
	}

	importedCount, err := svc.Import(ctx, ScopeChat, 888, map[string]map[string]string{
		"afk": {"auto_reply": "false"},
	}, 999)
	if err != nil || importedCount != 1 {
		t.Fatalf("import failed: count=%d, err=%v", importedCount, err)
	}

	importedVal, _ := svc.ResolveBool(ctx, 0, 888, "afk", "auto_reply")
	if importedVal {
		t.Error("expected imported auto_reply to be false")
	}

	// 11. History
	history, err := svc.GetHistory(ctx, "core", "prefix", 10)
	if err != nil || len(history) == 0 {
		t.Fatalf("expected change history records, got %d (err=%v)", len(history), err)
	}

	// Give event bus worker a moment to process events
	time.Sleep(50 * time.Millisecond)
	evMu.Lock()
	recLen := len(eventsReceived)
	evMu.Unlock()
	if recLen == 0 {
		t.Error("expected at least 1 SettingChangedEvent published to event bus")
	}
}

func TestScopeRef(t *testing.T) {
	g := GlobalScope()
	if err := g.Validate(); err != nil {
		t.Errorf("global scope validation failed: %v", err)
	}

	badG := ScopeRef{Type: ScopeGlobal, ID: 123}
	if err := badG.Validate(); err == nil {
		t.Errorf("expected error for non-zero global scope ID")
	}

	u := UserScope(456)
	if err := u.Validate(); err != nil {
		t.Errorf("user scope validation failed: %v", err)
	}

	badU := ScopeRef{Type: ScopeUser, ID: 0}
	if err := badU.Validate(); err == nil {
		t.Errorf("expected error for zero user scope ID")
	}

	c := ChatScope(789)
	if err := c.Validate(); err != nil {
		t.Errorf("chat scope validation failed: %v", err)
	}

	badC := ScopeRef{Type: ScopeChat, ID: 0}
	if err := badC.Validate(); err == nil {
		t.Errorf("expected error for zero chat scope ID")
	}
}

func TestSettingValue(t *testing.T) {
	vBool := NewSettingValue("true", nil)
	if !vBool.Bool() {
		t.Errorf("expected true, got %v", vBool.Bool())
	}
	if vBool.String() != "true" || vBool.Raw() != "true" {
		t.Errorf("expected 'true', got %s", vBool.String())
	}

	vInt := NewSettingValue("42", nil)
	if vInt.Int() != 42 {
		t.Errorf("expected 42, got %d", vInt.Int())
	}

	vDur := NewSettingValue("5m", nil)
	if vDur.Duration() != 5*time.Minute {
		t.Errorf("expected 5m, got %v", vDur.Duration())
	}

	vErr := NewSettingValue("", errors.New("missing"))
	if vErr.Err() == nil {
		t.Errorf("expected error to be preserved")
	}
	if vErr.Bool() != false || vErr.Int() != 0 || vErr.Duration() != 0 {
		t.Errorf("expected zero values when err is present")
	}
}

func TestImport_Atomicity(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry()
	_ = RegisterDefaultDefinitions(reg)
	repo := newMockRepo()
	svc := NewService(repo, reg, nil)

	// Attempt import with one valid and one INVALID setting (fails validation)
	invalidBatch := map[string]map[string]string{
		"afk": {
			"auto_reply": "true",
			"cooldown":   "not-a-valid-duration",
		},
	}

	count, err := svc.Import(ctx, ScopeGlobal, 0, invalidBatch, 1)
	if err == nil {
		t.Fatalf("expected import to fail due to invalid duration")
	}
	if count != 0 {
		t.Errorf("expected 0 imported items on failure, got %d", count)
	}

	// Verify that the valid setting was NOT written (atomic rollback / pre-validation)
	val, err := repo.GetSetting(ctx, string(ScopeGlobal), 0, "afk", "auto_reply")
	if err != nil {
		t.Fatalf("failed to query repo: %v", err)
	}
	if val != nil {
		t.Errorf("expected auto_reply NOT to be written due to batch atomicity, found: %v", val)
	}
}

func TestServiceScopeValidation(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepo()
	svc := NewService(repo, nil, nil)

	// Global scope with non-zero ID must fail
	if err := svc.Set(ctx, ScopeGlobal, 123, "core", "prefix", ".", 1); err == nil {
		t.Error("expected Set with ScopeGlobal and non-zero ID to fail")
	}
	if _, err := svc.Get(ctx, ScopeGlobal, 123, "core", "prefix"); err == nil {
		t.Error("expected Get with ScopeGlobal and non-zero ID to fail")
	}
	if err := svc.Reset(ctx, ScopeGlobal, 123, "core", "prefix", 1); err == nil {
		t.Error("expected Reset with ScopeGlobal and non-zero ID to fail")
	}
	if _, err := svc.ListByScope(ctx, ScopeGlobal, 123, "core"); err == nil {
		t.Error("expected ListByScope with ScopeGlobal and non-zero ID to fail")
	}
	if _, err := svc.Export(ctx, ScopeGlobal, 123); err == nil {
		t.Error("expected Export with ScopeGlobal and non-zero ID to fail")
	}
	if _, err := svc.Import(ctx, ScopeGlobal, 123, map[string]map[string]string{"core": {"prefix": "."}}, 1); err == nil {
		t.Error("expected Import with ScopeGlobal and non-zero ID to fail")
	}

	// Chat scope with zero ID must fail
	if err := svc.Set(ctx, ScopeChat, 0, "core", "prefix", ".", 1); err == nil {
		t.Error("expected Set with ScopeChat and zero ID to fail")
	}
	if _, err := svc.Get(ctx, ScopeChat, 0, "core", "prefix"); err == nil {
		t.Error("expected Get with ScopeChat and zero ID to fail")
	}
	if err := svc.Reset(ctx, ScopeChat, 0, "core", "prefix", 1); err == nil {
		t.Error("expected Reset with ScopeChat and zero ID to fail")
	}
}

func TestServiceResolverCacheAndInvalidation(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepo()
	reg := NewRegistry()
	_ = RegisterDefaultDefinitions(reg)
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatalf("start event bus: %v", err)
	}
	defer func() { _ = bus.Close() }()
	svc := NewService(repo, reg, bus)

	// 1. Initial resolution populates cache with default
	val, err := svc.Resolve(ctx, 10, 20, "core", "prefix")
	if err != nil || val != "." {
		t.Fatalf("expected default '.', got %q (err=%v)", val, err)
	}

	// 2. Setting override invalidates cache
	if err := svc.Set(ctx, ScopeChat, 20, "core", "prefix", "#", 10); err != nil {
		t.Fatalf("failed to set chat prefix: %v", err)
	}
	val, err = svc.Resolve(ctx, 10, 20, "core", "prefix")
	if err != nil || val != "#" {
		t.Fatalf("expected resolved '#', got %q", val)
	}

	// 3. Reset invalidates cache
	if err := svc.Reset(ctx, ScopeChat, 20, "core", "prefix", 10); err != nil {
		t.Fatalf("failed to reset chat prefix: %v", err)
	}
	val, err = svc.Resolve(ctx, 10, 20, "core", "prefix")
	if err != nil || val != "." {
		t.Fatalf("expected resolved fallback to '.', got %q", val)
	}

	// 4. External event invalidates cache
	_ = svc.Set(ctx, ScopeGlobal, 0, "core", "prefix", "$", 1)
	val, _ = svc.Resolve(ctx, 10, 20, "core", "prefix")
	if val != "$" {
		t.Fatalf("expected '$', got %q", val)
	}

	// Publish SettingChangedEvent via bus
	bus.Publish(&core.SettingChangedEvent{
		ScopeType: string(ScopeGlobal),
		ScopeID:   0,
		Namespace: "core",
		Key:       "prefix",
		NewVal:    "%",
	})
	// Give worker a brief moment to process event
	time.Sleep(10 * time.Millisecond)

	// Cache was invalidated, now query returns repo/new setting if we updated repo
	_ = repo.SetSetting(ctx, &database.SettingItem{
		ScopeType: string(ScopeGlobal),
		ScopeID:   0,
		Namespace: "core",
		Key:       "prefix",
		Value:     "%",
		ValueType: string(TypeString),
	})
	val, _ = svc.Resolve(ctx, 10, 20, "core", "prefix")
	if val != "%" {
		t.Fatalf("expected event-invalidated resolution '%%', got %q", val)
	}
}
