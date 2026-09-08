package afk

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/database"
)

func TestAFKFeatureMigrationFreshDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("run afk feature migration: %v", err)
	}

	repo := NewSQLiteRepository(db)
	if err := repo.SetAFK(ctx, 12345, true, "testing afk"); err != nil {
		t.Fatalf("set afk: %v", err)
	}

	status, err := repo.GetAFK(ctx, 12345)
	if err != nil {
		t.Fatalf("get afk: %v", err)
	}
	if status == nil || !status.IsAFK || status.Reason != "testing afk" {
		t.Fatalf("unexpected status: %#v", status)
	}

	if err := repo.SetAFK(ctx, 12345, false, ""); err != nil {
		t.Fatalf("deactivate afk: %v", err)
	}
	status, err = repo.GetAFK(ctx, 12345)
	if err != nil {
		t.Fatalf("get afk: %v", err)
	}
	if status != nil && status.IsAFK {
		t.Fatalf("expected inactive afk, got %#v", status)
	}
}

func TestAFKFeatureMigrationAdoptLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Legacy version 1 creates afk_status table
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("adopt afk legacy migration: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id = 'afk.001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 adopted feature migration, got %d", count)
	}
}
