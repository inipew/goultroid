package savedresponse

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

type MigrationProvider struct{}

type migration001 struct{}

var (
	_ database.MigrationProvider        = MigrationProvider{}
	_ database.SchemaInvariantMigration = migration001{}
)

func (MigrationProvider) Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}

func (migration001) ID() string { return "savedresponse.001" }

func (migration001) Description() string {
	return "Persistent SavedResponse surface bindings"
}

func (migration001) Checksum() string {
	return "225b33c972694bd87614126b44aa3439e73f00cf5e9a6a971a91132526255a67"
}

func (migration001) LegacyVersions() []int { return nil }

func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS saved_response_surface_bindings (
			surface TEXT NOT NULL CHECK (surface IN ('assistant_command', 'inline', 'deep_link', 'callback')),
			alias TEXT NOT NULL,
			provider TEXT NOT NULL,
			provider_scope_id INTEGER NOT NULL DEFAULT 0,
			provider_key TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT 1,
			revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			PRIMARY KEY (surface, alias)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_saved_response_surface_bindings_provider
			ON saved_response_surface_bindings(provider, provider_scope_id, provider_key);`,
		`CREATE INDEX IF NOT EXISTS idx_saved_response_surface_bindings_enabled
			ON saved_response_surface_bindings(surface, enabled, alias);`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var tableCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'saved_response_surface_bindings'
	`).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount != 1 {
		return fmt.Errorf("required table saved_response_surface_bindings does not exist")
	}
	for _, index := range []string{
		"idx_saved_response_surface_bindings_provider",
		"idx_saved_response_surface_bindings_enabled",
	} {
		var count int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM sqlite_master
			WHERE type = 'index' AND name = ?
		`, index).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("required index %s does not exist", index)
		}
	}
	return nil
}
