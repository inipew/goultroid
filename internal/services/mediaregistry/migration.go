package mediaregistry

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

func (migration001) ID() string          { return "mediaregistry.001" }
func (migration001) Description() string { return "Global media ownership and reference registry" }
func (migration001) Checksum() string {
	return "cff5519e349381206d0072b3c546c6826149c4f02b47e4c286ed0a0c060fbae6"
}
func (migration001) LegacyVersions() []int { return nil }

func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS media_assets (
			asset_id TEXT PRIMARY KEY,
			producer TEXT NOT NULL,
			owner TEXT NOT NULL,
			lifecycle TEXT NOT NULL,
			registered_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_media_assets_owner_lifecycle
			ON media_assets(owner, lifecycle, registered_at);`,
		`CREATE TABLE IF NOT EXISTS media_asset_references (
			asset_id TEXT NOT NULL,
			subsystem TEXT NOT NULL,
			reference_kind TEXT NOT NULL,
			reference_key TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			PRIMARY KEY (asset_id, subsystem, reference_kind, reference_key)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_media_asset_references_asset
			ON media_asset_references(asset_id);`,
		`CREATE INDEX IF NOT EXISTS idx_media_asset_references_subsystem
			ON media_asset_references(subsystem, reference_kind, reference_key);`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	for _, table := range []string{"media_assets", "media_asset_references"} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("required table %s does not exist", table)
		}
	}
	return nil
}
