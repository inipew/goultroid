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
	return "1a9b9b18a3a7b1e896e6ab0bdef774605f0d70ffbc0b3cb2c54985c86582692c"
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
