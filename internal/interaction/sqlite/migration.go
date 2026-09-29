package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/inipew/goultroid/internal/database"
)

const interactionSessionsSchema = `CREATE TABLE IF NOT EXISTS interaction_sessions (
	id TEXT PRIMARY KEY, feature_id TEXT NOT NULL, version TEXT NOT NULL,
	actor_id INTEGER NOT NULL, chat_id INTEGER NOT NULL, message_id INTEGER NOT NULL,
	inline_message_id TEXT NOT NULL, state BLOB NOT NULL, revision INTEGER NOT NULL,
	created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, input_expires_at INTEGER NOT NULL DEFAULT 0
)`

type MigrationProvider struct{}

type migration001 struct{}

var (
	_ database.MigrationProvider        = MigrationProvider{}
	_ database.SchemaInvariantMigration = migration001{}
)

func (MigrationProvider) Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}

func (migration001) ID() string { return "interaction.001" }

func (migration001) Description() string {
	return "Durable A2 interaction sessions"
}

func (migration001) Checksum() string {
	return "e44b0507936773a84f76c692973d2e6e6d760f5e9c4bea50ef84d49290a0aca1"
}

func (migration001) LegacyVersions() []int { return nil }

func (m migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	if _, err := tx.ExecContext(ctx, interactionSessionsSchema); err != nil {
		return err
	}
	return m.VerifySchema(ctx, tx)
}

type schemaColumn struct {
	name       string
	typeName   string
	notNull    int
	primaryKey int
	defaultVal string
}

func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info('interaction_sessions')`)
	if err != nil {
		return err
	}
	defer rows.Close()

	expected := []schemaColumn{
		{name: "id", typeName: "TEXT", primaryKey: 1},
		{name: "feature_id", typeName: "TEXT", notNull: 1},
		{name: "version", typeName: "TEXT", notNull: 1},
		{name: "actor_id", typeName: "INTEGER", notNull: 1},
		{name: "chat_id", typeName: "INTEGER", notNull: 1},
		{name: "message_id", typeName: "INTEGER", notNull: 1},
		{name: "inline_message_id", typeName: "TEXT", notNull: 1},
		{name: "state", typeName: "BLOB", notNull: 1},
		{name: "revision", typeName: "INTEGER", notNull: 1},
		{name: "created_at", typeName: "INTEGER", notNull: 1},
		{name: "expires_at", typeName: "INTEGER", notNull: 1},
		{name: "input_expires_at", typeName: "INTEGER", notNull: 1, defaultVal: "0"},
	}

	actual := make([]schemaColumn, 0, len(expected))
	for rows.Next() {
		var (
			cid        int
			column     schemaColumn
			defaultVal sql.NullString
		)
		if err := rows.Scan(&cid, &column.name, &column.typeName, &column.notNull, &defaultVal, &column.primaryKey); err != nil {
			return err
		}
		column.typeName = strings.ToUpper(strings.TrimSpace(column.typeName))
		if defaultVal.Valid {
			column.defaultVal = strings.TrimSpace(defaultVal.String)
		}
		actual = append(actual, column)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("interaction_sessions schema has %d columns, want %d", len(actual), len(expected))
	}
	for i := range expected {
		if actual[i] != expected[i] {
			return fmt.Errorf("interaction_sessions column %d = %+v, want %+v", i, actual[i], expected[i])
		}
	}
	return nil
}
