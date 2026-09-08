package blacklist

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
)

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

func Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}
