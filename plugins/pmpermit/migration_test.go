package pmpermit

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/database"
)

func TestPMPermitFeatureMigrationFreshDatabase(t *testing.T) {
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
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id = 'pmpermit.001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 feature migration, got %d", count)
	}

	var colCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('pm_permit_records') WHERE name='warn_msg_ids'`).Scan(&colCount); err != nil {
		t.Fatal(err)
	}
	if colCount != 1 {
		t.Fatalf("expected warn_msg_ids column, got %d matches", colCount)
	}
}

func TestPMPermitFeatureMigrationAdoptLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	// database.Open runs legacy migrations (including legacy 4 and 7 which created pm_permit_records and warn_msg_ids)
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("adopt legacy migrations: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id = 'pmpermit.001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 adopted feature migration, got %d", count)
	}
}
