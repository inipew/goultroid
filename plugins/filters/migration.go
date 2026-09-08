package filters

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
)

type migration001 struct{}

func (migration001) ID() string          { return "filters.001" }
func (migration001) Description() string { return "Persistent chat auto-reply filters" }
func (migration001) Checksum() string {
	return "c5e533b765ca5bb2ce95450415a7702f306646549a9979313b41315b93895e6e"
}
func (migration001) LegacyVersions() []int { return []int{1} }
func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS filters (
			chat_id INTEGER NOT NULL,
			keyword TEXT NOT NULL,
			reply_text TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			PRIMARY KEY (chat_id, keyword)
		);`)
	return err
}

func Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}
