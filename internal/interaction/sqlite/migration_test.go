package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/database"
)

const baselineInteractionSessionsSchema = `CREATE TABLE interaction_sessions (
	id TEXT PRIMARY KEY, feature_id TEXT NOT NULL, version TEXT NOT NULL,
	actor_id INTEGER NOT NULL, chat_id INTEGER NOT NULL, message_id INTEGER NOT NULL,
	inline_message_id TEXT NOT NULL, state BLOB NOT NULL, revision INTEGER NOT NULL,
	created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, input_expires_at INTEGER NOT NULL DEFAULT 0
)`

func TestMigrationCreatesInteractionSchemaIdempotently(t *testing.T) {
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
		SELECT count(*) FROM feature_schema_migrations WHERE id = 'interaction.001'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("interaction.001 migration records = %d, want 1", count)
	}
}

func TestMigrationAdoptsBaselineSchemaWithoutLosingRows(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, baselineInteractionSessionsSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO interaction_sessions (
			id, feature_id, version, actor_id, chat_id, message_id,
			inline_message_id, state, revision, created_at, expires_at, input_expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "abcdefghijklmnopqrstuv", "demo", "1", int64(7), int64(8), int64(9), "", []byte("persisted"), uint64(2), int64(100), int64(200), int64(150)); err != nil {
		t.Fatal(err)
	}

	if err := database.RunFeatureMigrations(ctx, db, MigrationProvider{}); err != nil {
		t.Fatalf("adopt baseline interaction schema: %v", err)
	}

	var (
		state    []byte
		revision uint64
	)
	if err := db.QueryRowContext(ctx, `
		SELECT state, revision FROM interaction_sessions WHERE id = ?
	`, "abcdefghijklmnopqrstuv").Scan(&state, &revision); err != nil {
		t.Fatal(err)
	}
	if string(state) != "persisted" || revision != 2 {
		t.Fatalf("baseline row after migration state=%q revision=%d", state, revision)
	}

	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM feature_schema_migrations WHERE id = 'interaction.001'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("interaction.001 migration records = %d, want 1", count)
	}
}

func TestMigrationRejectsMalformedPreexistingSchema(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `CREATE TABLE interaction_sessions (
		id TEXT PRIMARY KEY,
		feature_id TEXT NOT NULL,
		version TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}

	err = database.RunFeatureMigrations(ctx, db, MigrationProvider{})
	if err == nil {
		t.Fatal("expected malformed interaction_sessions schema to be rejected")
	}
	if !strings.Contains(err.Error(), "interaction_sessions schema") {
		t.Fatalf("migration error = %v, want schema diagnostic", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM feature_schema_migrations WHERE id = 'interaction.001'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("malformed schema recorded migration count = %d, want 0", count)
	}
}
