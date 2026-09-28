package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/tasks"
)

type restartCatalog struct{ generation uint64 }

func (c restartCatalog) FeatureScope(string) (tasks.ScopeIdentity, bool) {
	return tasks.ScopeIdentity{Owner: "plugin:demo", Generation: c.generation}, true
}
func (c restartCatalog) HasAction(_, action string) bool { return action == "next" }
func (c restartCatalog) DurabilityVersion(string) string { return "1" }

func TestSQLiteRuntimeRestartKeepsCallback(t *testing.T) {
	path := t.TempDir() + "/bot.db"
	firstDB, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	firstStore := NewStore(firstDB.DB)
	if err := firstStore.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := interaction.NewRuntime(restartCatalog{generation: 1}, interaction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetDurableStore(firstStore); err != nil {
		t.Fatal(err)
	}
	created, err := first.Create(context.Background(), interaction.CreateRequest{FeatureID: "demo", Binding: interaction.Binding{ActorID: 7}, State: []byte("persisted"), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	data, err := first.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	if err := firstDB.Close(); err != nil {
		t.Fatal(err)
	}
	secondDB, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()
	secondStore := NewStore(secondDB.DB)
	if err := secondStore.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := interaction.NewRuntime(restartCatalog{generation: 2}, interaction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.SetDurableStore(secondStore); err != nil {
		t.Fatal(err)
	}
	if err := second.RestoreDurable(context.Background()); err != nil {
		t.Fatal(err)
	}
	resolved, err := second.ResolveCallback(context.Background(), data, interaction.Binding{ActorID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Session.Scope.Generation != 2 || string(resolved.Session.State) != "persisted" {
		t.Fatalf("resolved = %+v", resolved.Session)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.DB)
	if err := store.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	row := interaction.DurableSession{Session: interaction.Session{ID: "abcdefghijklmnopqrstuv", FeatureID: "demo", Binding: interaction.Binding{ActorID: 7, ChatID: 8, MessageID: 9}, State: []byte("abc"), Revision: 2, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, Version: "1"}
	if err := store.Save(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Version != "1" || string(rows[0].Session.State) != "abc" || rows[0].Session.Binding.ChatID != 8 {
		t.Fatalf("rows = %+v", rows)
	}
	if err := store.Delete(context.Background(), row.Session.ID); err != nil {
		t.Fatal(err)
	}
	rows, err = store.Load(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("deleted rows = %+v, %v", rows, err)
	}
}

func TestStoreAcceptsEmptyState(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.DB)
	if err := store.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	row := interaction.DurableSession{Session: interaction.Session{ID: "abcdefghijklmnopqrstuv", FeatureID: "demo", Binding: interaction.Binding{ActorID: 7}, Revision: 1, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, Version: "1"}
	if err := store.Save(context.Background(), row); err != nil {
		t.Fatal(err)
	}
}
