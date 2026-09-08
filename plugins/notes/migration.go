package notes

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
)

type migration001 struct{}

func (migration001) ID() string          { return "notes.001" }
func (migration001) Description() string { return "Persistent chat notes storage" }
func (migration001) Checksum() string {
	return "8a9b2b5d4f186358c89bdf3ad7a87e0766322ad4f7c2290f6797a7eec437e24b"
}
func (migration001) LegacyVersions() []int { return []int{1} }
func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS notes (
			chat_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			PRIMARY KEY (chat_id, name)
		);`)
	return err
}

func Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}
