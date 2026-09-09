package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/platform/audit"
	_ "modernc.org/sqlite"
)

func setupTestStorageDB(t *testing.T) (*Manager, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mgr := NewManager(db)
	if err := mgr.InitSchema(context.Background()); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	return mgr, db
}

func TestStorage_SQLiteNamespaceIsolation(t *testing.T) {
	mgr, _ := setupTestStorageDB(t)
	ctx := context.Background()

	storeA := mgr.Store("plugin_a", true, true)
	storeB := mgr.Store("plugin_b", true, true)

	// A writes key1
	if err := storeA.Set(ctx, "key1", []byte("val_a")); err != nil {
		t.Fatalf("storeA set: %v", err)
	}

	// B writes key1 with different value
	if err := storeB.Set(ctx, "key1", []byte("val_b")); err != nil {
		t.Fatalf("storeB set: %v", err)
	}

	// A reads key1 -> val_a
	valA, err := storeA.Get(ctx, "key1")
	if err != nil || string(valA) != "val_a" {
		t.Fatalf("expected val_a, got %s (err: %v)", string(valA), err)
	}

	// B reads key1 -> val_b
	valB, err := storeB.Get(ctx, "key1")
	if err != nil || string(valB) != "val_b" {
		t.Fatalf("expected val_b, got %s (err: %v)", string(valB), err)
	}

	// A deletes key1 -> B still has key1
	if err := storeA.Delete(ctx, "key1"); err != nil {
		t.Fatalf("storeA delete: %v", err)
	}

	_, err = storeA.Get(ctx, "key1")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for storeA, got: %v", err)
	}

	valBAfter, err := storeB.Get(ctx, "key1")
	if err != nil || string(valBAfter) != "val_b" {
		t.Fatalf("storeB key1 must not be affected by storeA delete, got %s", string(valBAfter))
	}
}

func TestStorage_Permissions(t *testing.T) {
	mgr, _ := setupTestStorageDB(t)
	ctx := context.Background()

	readOnly := mgr.Store("reader", true, false)
	writeOnly := mgr.Store("writer", false, true)

	// ReadOnly cannot write or delete
	if err := readOnly.Set(ctx, "k", []byte("v")); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("expected ErrReadOnly for Set, got: %v", err)
	}
	if err := readOnly.Delete(ctx, "k"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("expected ErrReadOnly for Delete, got: %v", err)
	}

	// WriteOnly can set but cannot read or list
	if err := writeOnly.Set(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("expected writer to be able to set: %v", err)
	}
	if _, err := writeOnly.Get(ctx, "k"); !errors.Is(err, ErrWriteOnly) {
		t.Fatalf("expected ErrWriteOnly for Get, got: %v", err)
	}
	if _, err := writeOnly.List(ctx, ""); !errors.Is(err, ErrWriteOnly) {
		t.Fatalf("expected ErrWriteOnly for List, got: %v", err)
	}
}

func TestStorage_ListPrefix(t *testing.T) {
	mgr, _ := setupTestStorageDB(t)
	ctx := context.Background()
	store := mgr.Store("test", true, true)

	_ = store.Set(ctx, "config.theme", []byte("dark"))
	_ = store.Set(ctx, "config.lang", []byte("en"))
	_ = store.Set(ctx, "cache.user1", []byte("data"))

	listConfig, err := store.List(ctx, "config.")
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	if len(listConfig) != 2 {
		t.Fatalf("expected 2 config items, got %d", len(listConfig))
	}
	if string(listConfig["config.theme"]) != "dark" || string(listConfig["config.lang"]) != "en" {
		t.Errorf("unexpected config map: %+v", listConfig)
	}

	listAll, err := store.List(ctx, "")
	if err != nil || len(listAll) != 3 {
		t.Fatalf("expected 3 total items, got %d (err: %v)", len(listAll), err)
	}
}

func TestStorage_MemoryFallback(t *testing.T) {
	mgr := NewManager(nil) // nil db -> memory mode
	ctx := context.Background()

	storeA := mgr.Store("mod_a", true, true)
	storeB := mgr.Store("mod_b", true, true)

	_ = storeA.Set(ctx, "hello", []byte("world"))
	_ = storeB.Set(ctx, "hello", []byte("friend"))

	valA, _ := storeA.Get(ctx, "hello")
	valB, _ := storeB.Get(ctx, "hello")

	if string(valA) != "world" || string(valB) != "friend" {
		t.Fatalf("memory store namespace mismatch: A=%s, B=%s", valA, valB)
	}
}

type mockAuditor struct {
	events []string
}

func (m *mockAuditor) Record(ctx context.Context, e audit.AuditEvent) error {
	m.events = append(m.events, e.Action+":"+e.Target)
	return nil
}

func (m *mockAuditor) Recent(limit int) []audit.AuditEvent {
	return nil
}

func TestStorage_Auditor(t *testing.T) {
	mgr, _ := setupTestStorageDB(t)
	mock := &mockAuditor{}
	mgr.SetAuditor(mock)

	ctx := context.Background()
	store := mgr.Store("test_mod", true, true)

	_ = store.Set(ctx, "mykey", []byte("val"))
	_ = store.Delete(ctx, "mykey")

	roStore := mgr.Store("test_mod", true, false)
	_ = roStore.Set(ctx, "denied_key", []byte("val"))

	woStore := mgr.Store("test_mod", false, true)
	_, _ = woStore.Get(ctx, "denied_key")

	expected := []string{
		"storage.write:test_mod/mykey",
		"storage.delete:test_mod/mykey",
		"storage.write.denied:test_mod/denied_key",
		"storage.read.denied:test_mod/denied_key",
	}

	if len(mock.events) != len(expected) {
		t.Fatalf("expected %d events, got %d: %+v", len(expected), len(mock.events), mock.events)
	}
	for i, exp := range expected {
		if mock.events[i] != exp {
			t.Errorf("[%d] expected %s, got %s", i, exp, mock.events[i])
		}
	}
}
