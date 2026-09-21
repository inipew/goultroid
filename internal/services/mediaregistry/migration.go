package mediaregistry

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

type MigrationProvider struct{}

type migration001 struct{}
type migration002 struct{}

var (
	_ database.MigrationProvider        = MigrationProvider{}
	_ database.SchemaInvariantMigration = migration001{}
	_ database.SchemaInvariantMigration = migration002{}
)

func (MigrationProvider) Migrations() []database.Migration {
	return []database.Migration{migration001{}, migration002{}}
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

func (migration002) ID() string { return "mediaregistry.002" }
func (migration002) Description() string {
	return "Durable safe media reclamation intents and reference interlocks"
}
func (migration002) Checksum() string {
	return "15857c317b743e87282c8777a6bb105812f073e67a4d9ee91e48780099c4949c"
}
func (migration002) LegacyVersions() []int { return nil }

func (migration002) Up(ctx context.Context, tx database.SQLExecutor) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS media_reclamation_intents (
			asset_id TEXT PRIMARY KEY,
			expected_owner TEXT NOT NULL,
			expected_lifecycle TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL CHECK (state IN ('prepared', 'pending', 'deleting')),
			attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
			last_error TEXT NOT NULL DEFAULT '',
			not_before DATETIME NOT NULL,
			next_attempt_at DATETIME NOT NULL,
			claim_token TEXT NOT NULL DEFAULT '',
			claimed_at DATETIME,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_media_reclamation_intents_due
			ON media_reclamation_intents(state, next_attempt_at, created_at);`,
		`CREATE INDEX IF NOT EXISTS idx_media_reclamation_intents_claimed
			ON media_reclamation_intents(state, claimed_at);`,
		`CREATE TRIGGER IF NOT EXISTS trg_media_reclaim_ref_insert_block
			BEFORE INSERT ON media_asset_references
			WHEN EXISTS (
				SELECT 1 FROM media_reclamation_intents
				WHERE asset_id = NEW.asset_id AND state = 'deleting'
			)
			BEGIN
				SELECT RAISE(ABORT, 'media reclamation in progress');
			END;`,
		`CREATE TRIGGER IF NOT EXISTS trg_media_reclaim_ref_insert_cancel
			AFTER INSERT ON media_asset_references
			BEGIN
				DELETE FROM media_reclamation_intents
				WHERE asset_id = NEW.asset_id AND state IN ('prepared', 'pending');
			END;`,
		`CREATE TRIGGER IF NOT EXISTS trg_media_reclaim_ref_asset_update_block
			BEFORE UPDATE OF asset_id ON media_asset_references
			WHEN EXISTS (
				SELECT 1 FROM media_reclamation_intents
				WHERE asset_id = NEW.asset_id AND state = 'deleting'
			)
			BEGIN
				SELECT RAISE(ABORT, 'media reclamation in progress');
			END;`,
		`CREATE TRIGGER IF NOT EXISTS trg_media_reclaim_ref_asset_update_cancel
			AFTER UPDATE OF asset_id ON media_asset_references
			BEGIN
				DELETE FROM media_reclamation_intents
				WHERE asset_id = NEW.asset_id AND state IN ('prepared', 'pending');
			END;`,
		`CREATE TRIGGER IF NOT EXISTS trg_media_reclaim_asset_policy_update_block
			BEFORE UPDATE OF producer, owner, lifecycle ON media_assets
			WHEN EXISTS (
				SELECT 1 FROM media_reclamation_intents
				WHERE asset_id = OLD.asset_id AND state = 'deleting'
			)
			BEGIN
				SELECT RAISE(ABORT, 'media reclamation in progress');
			END;`,
		`CREATE TRIGGER IF NOT EXISTS trg_media_reclaim_asset_delete_block
			BEFORE DELETE ON media_assets
			WHEN EXISTS (
				SELECT 1 FROM media_reclamation_intents
				WHERE asset_id = OLD.asset_id AND state = 'deleting'
			)
			BEGIN
				SELECT RAISE(ABORT, 'media reclamation in progress');
			END;`,
		`CREATE TRIGGER IF NOT EXISTS trg_media_reclaim_asset_policy_update_cancel
			AFTER UPDATE OF owner, lifecycle ON media_assets
			BEGIN
				DELETE FROM media_reclamation_intents
				WHERE asset_id = NEW.asset_id
				  AND state IN ('prepared', 'pending')
				  AND (
					expected_owner <> NEW.owner
					OR expected_lifecycle <> NEW.lifecycle
					OR NEW.lifecycle = 'legacy'
				  );
			END;`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (migration002) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var tableCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'media_reclamation_intents'
	`).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount != 1 {
		return fmt.Errorf("required table media_reclamation_intents does not exist")
	}

	for _, trigger := range []string{
		"trg_media_reclaim_ref_insert_block",
		"trg_media_reclaim_ref_insert_cancel",
		"trg_media_reclaim_ref_asset_update_block",
		"trg_media_reclaim_ref_asset_update_cancel",
		"trg_media_reclaim_asset_policy_update_block",
		"trg_media_reclaim_asset_delete_block",
		"trg_media_reclaim_asset_policy_update_cancel",
	} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("required trigger %s does not exist", trigger)
		}
	}
	return nil
}
