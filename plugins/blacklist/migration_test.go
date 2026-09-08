package blacklist

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/database"
)

func TestBlacklistFeatureMigrationFreshDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("run blacklist feature migration: %v", err)
	}

	repo := NewSQLiteRepository(db)
	if err := repo.AddBlacklist(ctx, 100, "badword"); err != nil {
		t.Fatalf("add blacklist: %v", err)
	}

	list, err := repo.ListBlacklists(ctx, 100)
	if err != nil {
		t.Fatalf("list blacklists: %v", err)
	}
	if len(list) != 1 || list[0] != "badword" {
		t.Fatalf("unexpected list: %#v", list)
	}

	if err := repo.RemoveBlacklist(ctx, 100, "badword"); err != nil {
		t.Fatalf("remove blacklist: %v", err)
	}
}

func TestBlacklistFeatureMigrationAdoptLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Legacy version 1 creates blacklists table already
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("adopt blacklist legacy migration: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id = 'blacklist.001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 adopted feature migration, got %d", count)
	}
}
