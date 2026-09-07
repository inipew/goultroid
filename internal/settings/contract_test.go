package settings

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

// TestContract_SettingsHierarchy verifies chat > user > global > default fallback (bug13 #29, #42)
func TestContract_SettingsHierarchy(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	reg := NewRegistry()
	if err := RegisterDefaultDefinitions(reg); err != nil {
		t.Fatalf("register defaults: %v", err)
	}
	// Ensure test keys exist; use a known key from defaults e.g. "core:prefix" or create synthetic
	// Use existing default: core:prefix not guaranteed, so register synthetic for hierarchy test
	def := SettingDefinition{
		Namespace:    "contract",
		Key:          "level",
		Type:         TypeString,
		DefaultValue: "default",
		Title:        "Contract Level",
		Category:     CategoryGeneral,
	}
	if err := reg.Register(def); err != nil {
		t.Fatalf("register synthetic: %v", err)
	}

	bus := core.NewEventBus()
	defer func() { _ = bus.Close() }()
	svc := NewService(db, reg, bus)
	ctx := context.Background()

	// 1. No override -> default
	val, err := svc.Resolve(ctx, 111, 999, "contract", "level")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if val != "default" {
		t.Fatalf("expected default, got %q", val)
	}

	// 2. Global override
	if err := svc.Set(ctx, ScopeGlobal, 0, "contract", "level", "global_val", 1); err != nil {
		t.Fatalf("set global: %v", err)
	}
	val, _ = svc.Resolve(ctx, 111, 999, "contract", "level")
	if val != "global_val" {
		t.Fatalf("expected global_val, got %q", val)
	}

	// 3. User override takes precedence over global
	if err := svc.Set(ctx, ScopeUser, 111, "contract", "level", "user_val", 1); err != nil {
		t.Fatalf("set user: %v", err)
	}
	val, _ = svc.Resolve(ctx, 111, 999, "contract", "level")
	if val != "user_val" {
		t.Fatalf("expected user_val, got %q", val)
	}
	// Other user still sees global
	val, _ = svc.Resolve(ctx, 222, 999, "contract", "level")
	if val != "global_val" {
		t.Fatalf("other user expected global_val, got %q", val)
	}

	// 4. Chat override takes precedence over user+global
	if err := svc.Set(ctx, ScopeChat, 999, "contract", "level", "chat_val", 1); err != nil {
		t.Fatalf("set chat: %v", err)
	}
	val, _ = svc.Resolve(ctx, 111, 999, "contract", "level")
	if val != "chat_val" {
		t.Fatalf("expected chat_val, got %q", val)
	}
	// Other chat still sees user/global
	val, _ = svc.Resolve(ctx, 111, 888, "contract", "level")
	if val != "user_val" {
		t.Fatalf("other chat expected user_val, got %q", val)
	}
}

// TestContract_SettingsCacheInvalidation ensures Set invalidates cache and publishes event
func TestContract_SettingsCacheInvalidation(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	reg := NewRegistry()
	def := SettingDefinition{
		Namespace:    "contract",
		Key:          "cache",
		Type:         TypeString,
		DefaultValue: "d",
		Title:        "Cache Test",
		Category:     CategoryGeneral,
	}
	if err := reg.Register(def); err != nil {
		t.Fatalf("register: %v", err)
	}

	bus := core.NewEventBus()
	defer func() { _ = bus.Close() }()
	svc := NewService(db, reg, bus)
	ctx := context.Background()

	// Prime cache
	_, _ = svc.Resolve(ctx, 1, 1, "contract", "cache")

	// Listen for published event
	events := make(chan core.Event, 2)
	bus.Subscribe(core.EventTypeSettingChanged, func(e core.Event) {
		events <- e
	})

	if err := svc.Set(ctx, ScopeGlobal, 0, "contract", "cache", "new_val", 42); err != nil {
		t.Fatalf("set: %v", err)
	}

	// EventBus is async (8 workers, queue) - cache invalidation via bus is async.
	// Set() itself invalidates synchronously via s.invalidate before publish, so Resolve must see new_val immediately.
	val, _ := svc.Resolve(ctx, 1, 1, "contract", "cache")
	if val != "new_val" {
		t.Fatalf("cache not invalidated, got %q", val)
	}

	// Event published async - wait briefly
	select {
	case ev := <-events:
		se, ok := ev.(*core.SettingChangedEvent)
		if !ok {
			t.Fatalf("unexpected event type %T", ev)
		}
		if se.NewVal != "new_val" || se.ChangedBy != 42 {
			t.Fatalf("unexpected event %+v", se)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected SettingChangedEvent to be published")
	}
}

// TestContract_ScopeValidation verifies ScopeRef invariants end-to-end via Service
func TestContract_ScopeValidation(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	reg := NewRegistry()
	svc := NewService(db, reg, nil)
	ctx := context.Background()

	// Global with non-zero ID must fail
	if err := svc.Set(ctx, ScopeGlobal, 999, "ns", "k", "v", 1); err == nil {
		t.Fatalf("expected error for global with non-zero ID")
	}
	// Chat with zero ID must fail
	if err := svc.Set(ctx, ScopeChat, 0, "ns", "k", "v", 1); err == nil {
		t.Fatalf("expected error for chat with zero ID")
	}
	// User with zero ID must fail
	if err := svc.Set(ctx, ScopeUser, 0, "ns", "k", "v", 1); err == nil {
		t.Fatalf("expected error for user with zero ID")
	}
	// Valid should pass (global)
	if err := svc.Set(ctx, ScopeGlobal, 0, "ns", "k", "v", 1); err != nil {
		t.Fatalf("valid global set failed: %v", err)
	}
	// Reset must also validate actor consistency
	if err := svc.Reset(ctx, ScopeChat, 0, "ns", "k", 1); err == nil {
		t.Fatalf("expected error for reset chat with zero ID")
	}
}

// TestContract_ImportAtomic ensures Import validates all before persisting any
func TestContract_ImportAtomic(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	reg := NewRegistry()
	def := SettingDefinition{
		Namespace:    "contract",
		Key:          "num",
		Type:         TypeInt,
		DefaultValue: "0",
		MinVal:       int64Ptr(0),
		MaxVal:       int64Ptr(10),
		Title:        "Num",
		Category:     CategoryGeneral,
	}
	if err := reg.Register(def); err != nil {
		t.Fatalf("register: %v", err)
	}
	svc := NewService(db, reg, nil)
	ctx := context.Background()

	// Set initial value
	if err := svc.Set(ctx, ScopeGlobal, 0, "contract", "num", "5", 1); err != nil {
		t.Fatalf("set initial: %v", err)
	}

	// Import with one invalid -> all must be rejected, old value preserved
	// Invalid: num=99 exceeds max 10
	dataInvalid := map[string]map[string]string{
		"contract": {"num": "99"},
	}
	n, err := svc.Import(ctx, ScopeGlobal, 0, dataInvalid, 1)
	if err == nil {
		t.Fatalf("expected import to fail, got n=%d", n)
	}
	// Ensure old value still 5 (not overwritten)
	val, _ := svc.Resolve(ctx, 0, 0, "contract", "num")
	if val != "5" {
		t.Fatalf("atomic import violated: expected 5, got %q", val)
	}

	// Valid import should succeed
	n, err = svc.Import(ctx, ScopeGlobal, 0, map[string]map[string]string{"contract": {"num": "7"}}, 1)
	if err != nil {
		t.Fatalf("valid import failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected n=1, got %d", n)
	}
	val, _ = svc.Resolve(ctx, 0, 0, "contract", "num")
	if val != "7" {
		t.Fatalf("expected 7 after import, got %q", val)
	}
}

func int64Ptr(v int64) *int64 { return &v }
