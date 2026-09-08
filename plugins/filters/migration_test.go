package filters

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/database"
)

func TestFiltersFeatureMigrationFreshDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("run filters feature migration: %v", err)
	}

	repo := NewSQLiteRepository(db)
	if err := repo.SaveFilter(ctx, 100, "hello", "world"); err != nil {
		t.Fatalf("save filter: %v", err)
	}

	f, err := repo.GetFilter(ctx, 100, "hello")
	if err != nil {
		t.Fatalf("get filter: %v", err)
	}
	if f == nil || f.ReplyText != "world" {
		t.Fatalf("unexpected filter: %#v", f)
	}

	list, err := repo.ListFilters(ctx, 100)
	if err != nil {
		t.Fatalf("list filters: %v", err)
	}
	if len(list) != 1 || list[0].Keyword != "hello" {
		t.Fatalf("unexpected list: %#v", list)
	}

	if err := repo.DeleteFilter(ctx, 100, "hello"); err != nil {
		t.Fatalf("delete filter: %v", err)
	}
}

func TestFiltersFeatureMigrationAdoptLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Legacy version 1 creates filters table
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("adopt filters legacy migration: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id = 'filters.001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 adopted feature migration, got %d", count)
	}
}
