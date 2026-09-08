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
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.ExecContext(ctx, `CREATE TABLE clone_state (
		owner_id INTEGER PRIMARY KEY,
		original_first_name TEXT NOT NULL DEFAULT '',
		original_last_name TEXT NOT NULL DEFAULT '',
		original_bio TEXT NOT NULL DEFAULT '',
		original_photo_path TEXT NOT NULL DEFAULT '',
		active BOOLEAN NOT NULL DEFAULT 0,
		updated_at DATETIME NOT NULL,
		cloned_photo BOOLEAN NOT NULL DEFAULT 0
	);`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version, description, checksum, applied_at) VALUES (15, 'legacy clone', 'legacy', CURRENT_TIMESTAMP), (16, 'legacy clone photo', 'legacy', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

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
