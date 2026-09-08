package admin

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

type migration001 struct{}

func (migration001) ID() string          { return "admin.001" }
func (migration001) Description() string { return "Persistent moderation warnings" }
func (migration001) Checksum() string {
	return "0f2d4e50a3f0a2c0c3b9f0c4f2b6a6d8e9d0f0e2b7f5f1f4b6c1a3d2e8f4c9b7"
}
func (migration001) LegacyVersions() []int { return []int{8} }

func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS moderation_warnings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			warned_by INTEGER NOT NULL,
			created_at DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_moderation_warnings_chat_user
			ON moderation_warnings(chat_id, user_id);`)
	return err
}

func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var tableCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='moderation_warnings'`).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount == 0 {
		return fmt.Errorf("required table moderation_warnings does not exist")
	}
	return nil
}

func Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}

var _ database.MigrationProvider = ModuleType{}
var _ database.SchemaInvariantMigration = migration001{}
