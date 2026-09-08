package sudo

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

var _ database.SchemaInvariantMigration = migration001{}

type migration001 struct{}

func (migration001) ID() string          { return "sudo.001" }
func (migration001) Description() string { return "Persistent sudo users" }
func (migration001) Checksum() string {
	return "3f4fa666ab7c45f7be44ea275f9c5336cb2df2a86ac82dd34d5625ad45a2ee21"
}
func (migration001) LegacyVersions() []int { return []int{1} }
func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS sudo_users (
			user_id INTEGER PRIMARY KEY,
			added_at DATETIME NOT NULL,
			added_by INTEGER NOT NULL
		);`)
	return err
}
func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sudo_users'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required table sudo_users does not exist")
	}
	return nil
}

func Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}
