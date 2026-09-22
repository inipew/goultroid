package deeplink

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

type MigrationProvider struct{}
type migration001 struct{}

var (
	_ database.MigrationProvider         = MigrationProvider{}
	_ database.SchemaInvariantMigration = migration001{}
)

func (MigrationProvider) Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}

func (migration001) ID() string { return "assistant.deeplink.001" }
func (migration001) Description() string {
	return "Persistent typed Assistant deep-link tokens"
}
func (migration001) Checksum() string {
	return "1169e94635e91fe280ef95cb0c2110a80564a64ff61b32f754e9d082aa592c0e"
}
func (migration001) LegacyVersions() []int { return nil }

func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS assistant_deep_link_tokens (
			token TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			payload TEXT NOT NULL,
			actor_id INTEGER NOT NULL DEFAULT 0,
			single_use BOOLEAN NOT NULL DEFAULT 0,
			consumed_at DATETIME,
			expires_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_assistant_deep_link_tokens_expiry
			ON assistant_deep_link_tokens(expires_at, token);`,
		`CREATE INDEX IF NOT EXISTS idx_assistant_deep_link_tokens_kind
			ON assistant_deep_link_tokens(kind, expires_at);`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'assistant_deep_link_tokens'
	`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("required table assistant_deep_link_tokens does not exist")
	}
	for _, index := range []string{
		"idx_assistant_deep_link_tokens_expiry",
		"idx_assistant_deep_link_tokens_kind",
	} {
		count = 0
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM sqlite_master
			WHERE type = 'index' AND name = ?
		`, index).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("required index %s does not exist", index)
		}
	}
	return nil
}
