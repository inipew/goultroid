package sudo

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
)

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

func Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}
