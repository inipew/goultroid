package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// InitSchema initializes the assistant_deep_links table and its supporting indexes.
func InitSchema(ctx context.Context, db *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS assistant_deep_links (
			id               TEXT PRIMARY KEY,
			token_hash       BLOB NOT NULL UNIQUE,
			purpose          TEXT NOT NULL,
			owner            TEXT NOT NULL,
			generation       INTEGER NOT NULL,
			user_id          INTEGER NOT NULL DEFAULT 0,
			source_chat_id   INTEGER NOT NULL DEFAULT 0,
			screen_namespace TEXT NOT NULL DEFAULT '',
			screen_name      TEXT NOT NULL DEFAULT '',
			screen_version   INTEGER NOT NULL DEFAULT 0,
			payload_type     TEXT NOT NULL,
			payload_version  INTEGER NOT NULL,
			payload          BLOB NOT NULL,
			single_use       INTEGER NOT NULL,
			issued_at        INTEGER NOT NULL,
			expires_at       INTEGER NOT NULL,
			consumed_at      INTEGER,
			consumed_by      INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE INDEX IF NOT EXISTS assistant_deep_links_expiry_idx
		ON assistant_deep_links(expires_at);`,
		`CREATE INDEX IF NOT EXISTS assistant_deep_links_owner_generation_idx
		ON assistant_deep_links(owner, generation);`,
	}

	for _, q := range queries {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("execute deep link schema statement: %w", err)
		}
	}
	return nil
}
