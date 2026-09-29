package app

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/database"
)

func TestBuiltinFeatureMigrationsIncludeInteractionSessions(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := migrateBuiltinFeatures(ctx, db); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'interaction_sessions'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("interaction_sessions table count = %d, want 1", count)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM feature_schema_migrations
		WHERE id = 'interaction.001'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("interaction.001 migration records = %d, want 1", count)
	}
}
