package myxl

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

var _ database.SchemaInvariantMigration = migration001{}

type migration001 struct{}

func (migration001) ID() string          { return "myxl.001" }
func (migration001) Description() string { return "Persistent MyXL accounts and token storage" }
func (migration001) Checksum() string {
	return "3b81523612b636619cdbd544b705ea8401b04e6427f85b6b8178d6e26fe39942"
}
func (migration001) LegacyVersions() []int { return nil }

func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	query := `
	CREATE TABLE IF NOT EXISTS myxl_accounts (
		msisdn TEXT PRIMARY KEY,
		alias TEXT NOT NULL DEFAULT '',
		is_active INTEGER NOT NULL DEFAULT 0,
		access_token TEXT NOT NULL DEFAULT '',
		id_token TEXT NOT NULL DEFAULT '',
		refresh_token TEXT NOT NULL DEFAULT '',
		subscriber_id TEXT NOT NULL DEFAULT '',
		subscription_type TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_myxl_accounts_active ON myxl_accounts(is_active);
	`
	_, err := tx.ExecContext(ctx, query)
	return err
}

func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='myxl_accounts'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required table myxl_accounts does not exist")
	}
	return nil
}

// Migrations returns the database migrations for the myxl plugin.
func Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}
