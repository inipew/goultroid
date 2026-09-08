package notes

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/database"
)

func TestNotesFeatureMigrationFreshDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("run notes feature migration: %v", err)
	}

	repo := NewSQLiteRepository(db)
	if err := repo.SaveNote(ctx, 100, "hello", "world"); err != nil {
		t.Fatalf("save note: %v", err)
	}

	note, err := repo.GetNote(ctx, 100, "hello")
	if err != nil {
		t.Fatalf("get note: %v", err)
	}
	if note == nil || note.Content != "world" {
		t.Fatalf("unexpected note: %#v", note)
	}

	list, err := repo.ListNotes(ctx, 100)
	if err != nil {
		t.Fatalf("list notes: %v", err)
	}
	if len(list) != 1 || list[0] != "hello" {
		t.Fatalf("unexpected list: %#v", list)
	}

	if err := repo.DeleteNote(ctx, 100, "hello"); err != nil {
		t.Fatalf("delete note: %v", err)
	}
}

func TestNotesFeatureMigrationAdoptLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Legacy version 1 creates notes table already
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("adopt notes legacy migration: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id = 'notes.001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 adopted feature migration, got %d", count)
	}
}
