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
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE moderation_warnings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			warned_by INTEGER NOT NULL,
			created_at DATETIME NOT NULL
		);`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version, description, checksum, applied_at) VALUES (?, ?, ?, datetime('now'))`, 8, legacy.Description, legacy.CanonicalChecksum); err != nil {
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
		t.Fatalf("expected adopted admin.001 migration record, got %d", count)
	}
}
