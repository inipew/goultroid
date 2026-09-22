package deeplink

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

type MigrationProvider struct{}
type migration001 struct{}
type migration002 struct{}

var (
	_ database.MigrationProvider         = MigrationProvider{}
	_ database.SchemaInvariantMigration = migration001{}
	_ database.SchemaInvariantMigration = migration002{}
)

func (MigrationProvider) Migrations() []database.Migration {
	return []database.Migration{migration001{}, migration002{}}
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

func (migration002) ID() string { return "assistant.deeplink.002" }
func (migration002) Description() string {
	return "Recoverable single-use Assistant deep-link claims"
}
func (migration002) Checksum() string {
	return "da464284bca7c294632d69b10b7acb67fe5953f85a174e6063cc3203381ca5e8"
}
func (migration002) LegacyVersions() []int { return nil }

func (migration002) Up(ctx context.Context, tx database.SQLExecutor) error {
	for _, statement := range []string{
		`ALTER TABLE assistant_deep_link_tokens ADD COLUMN claim_id TEXT;`,
		`ALTER TABLE assistant_deep_link_tokens ADD COLUMN claim_expires_at DATETIME;`,
		`CREATE INDEX IF NOT EXISTS idx_assistant_deep_link_tokens_claim
			ON assistant_deep_link_tokens(claim_expires_at, token);`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (migration002) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM pragma_table_info('assistant_deep_link_tokens')
		WHERE name IN ('claim_id', 'claim_expires_at')
	`).Scan(&count); err != nil {
		return err
	}
	if count != 2 {
		return fmt.Errorf("required deep-link claim columns do not exist")
	}
	count = 0
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_assistant_deep_link_tokens_claim'
	`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("required index idx_assistant_deep_link_tokens_claim does not exist")
	}
	return nil
}
