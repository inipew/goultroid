package pmrelay

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/database"
)

func TestMigrationCreatesRelaySchemaIdempotently(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for i := 0; i < 2; i++ {
		if err := database.RunFeatureMigrations(ctx, db, MigrationProvider{}); err != nil {
			t.Fatalf("RunFeatureMigrations(%d) error = %v", i, err)
		}
	}
	if err := (migration001{}).VerifySchema(ctx, db); err != nil {
		t.Fatalf("VerifySchema() error = %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM feature_schema_migrations WHERE id = 'pmrelay.001'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("pmrelay migration records = %d, want 1", count)
	}
}
