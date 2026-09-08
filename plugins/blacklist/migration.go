package blacklist

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

var _ database.SchemaInvariantMigration = migration001{}

type migration001 struct{}

func (migration001) ID() string          { return "blacklist.001" }
func (migration001) Description() string { return "Persistent chat keyword blacklists" }
func (migration001) Checksum() string {
	return "8f19da5252aa8436c84b1eb411f5ee7dc8fc0d63503aa8bc4ee5604ca3cf40a8"
}
func (migration001) LegacyVersions() []int { return []int{1} }
func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS blacklists (
			chat_id INTEGER NOT NULL,
			word TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			PRIMARY KEY (chat_id, word)
		);`)
	return err
}
func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='blacklists'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required table blacklists does not exist")
	}
	return nil
}

func Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}
