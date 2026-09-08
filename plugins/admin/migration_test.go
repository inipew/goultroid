package admin

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/database"
)

func TestMigration001FreshDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 8`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE moderation_warnings`); err != nil {
		t.Fatal(err)
	}

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id = 'admin.001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected admin.001 migration record, got %d", count)
	}

	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='moderation_warnings'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 1 {
		t.Fatalf("expected moderation_warnings table, got %d", tableCount)
	}
}

func TestMigration001LegacyAdoption(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	legacy, ok := database.LookupLegacyMigration(8)
	if !ok {
		t.Fatal("legacy migration 8 not registered")
	}
	var legacyCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = 8`).Scan(&legacyCount); err != nil {
		t.Fatal(err)
	}
	if legacyCount != 1 {
		t.Fatalf("expected legacy migration 8, got %d records", legacyCount)
	}

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id = 'admin.001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected adopted admin.001 migration record, got %d", count)
	}

	var description string
	if err := db.QueryRowContext(ctx, `SELECT description FROM feature_schema_migrations WHERE id = 'admin.001'`).Scan(&description); err != nil {
		t.Fatal(err)
	}
	if description == "" || legacy.Description == "" {
		t.Fatal("expected non-empty migration descriptions")
	}
}
