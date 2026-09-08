package clone

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/database"
)

func TestFeatureMigrationsFreshDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("run feature migrations: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id IN ('clone.001', 'clone.002')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 feature migrations, got %d", count)
	}

	var clonedPhoto int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('clone_state') WHERE name='cloned_photo'`).Scan(&clonedPhoto); err != nil {
		t.Fatal(err)
	}
	if clonedPhoto != 1 {
		t.Fatalf("expected cloned_photo column, got %d matches", clonedPhoto)
	}
}

func TestFeatureMigrationsAdoptLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	// database.Open(":memory:") runs legacy migrations 1..16
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Before feature migrations, schema_migrations has 15 and 16
	var legacyCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version IN (15, 16)`).Scan(&legacyCount); err != nil {
		t.Fatal(err)
	}
	if legacyCount != 2 {
		t.Fatalf("expected 2 legacy migrations in schema_migrations, got %d", legacyCount)
	}

	// Running feature migrations should adopt legacy versions 15 and 16 safely
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("adopt legacy migrations: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id IN ('clone.001', 'clone.002')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 adopted feature migrations, got %d", count)
	}
}

func TestFeatureMigrationsTamperedLegacyChecksum(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Tamper recorded checksum of legacy migration 15
	if _, err := db.ExecContext(ctx, `UPDATE schema_migrations SET checksum = 'tampered_legacy_hash' WHERE version = 15`); err != nil {
		t.Fatal(err)
	}

	err = database.RunFeatureMigrations(ctx, db, Module)
	if err == nil {
		t.Fatal("expected failure on tampered legacy checksum, got nil")
	}
}

func TestFeatureMigrationsMissingSchemaInvariant(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Drop the table while keeping version 15 in schema_migrations
	if _, err := db.ExecContext(ctx, `DROP TABLE clone_state`); err != nil {
		t.Fatal(err)
	}

	err = database.RunFeatureMigrations(ctx, db, Module)
	if err == nil {
		t.Fatal("expected failure on missing schema invariant table, got nil")
	}
}

func TestFeatureMigrationsIdempotentRerun(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Second run must be clean and idempotent
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("second run: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id IN ('clone.001', 'clone.002')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 feature migrations, got %d", count)
	}
}

func TestFeatureMigrationsTamperedFeatureChecksum(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("initial run: %v", err)
	}

	// Tamper recorded checksum in feature_schema_migrations
	if _, err := db.ExecContext(ctx, `UPDATE feature_schema_migrations SET checksum = 'tampered' WHERE id = 'clone.001'`); err != nil {
		t.Fatal(err)
	}

	err = database.RunFeatureMigrations(ctx, db, Module)
	if err == nil {
		t.Fatal("expected failure on tampered feature migration checksum, got nil")
	}
}
