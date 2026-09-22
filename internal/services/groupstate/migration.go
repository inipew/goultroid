package groupstate

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

type MigrationProvider struct{}

type migration001 struct{}
type migration002 struct{}
type migration003 struct{}

var _ database.SchemaInvariantMigration = migration001{}
var _ database.SchemaInvariantMigration = migration002{}
var _ database.SchemaInvariantMigration = migration003{}

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS assistant_group_state (
		chat_id INTEGER NOT NULL CHECK (chat_id > 0),
		namespace TEXT NOT NULL CHECK (length(namespace) BETWEEN 1 AND 64),
		key TEXT NOT NULL CHECK (length(key) BETWEEN 1 AND 128),
		value BLOB NOT NULL,
		revision INTEGER NOT NULL CHECK (revision > 0),
		updated_by INTEGER NOT NULL CHECK (updated_by > 0),
		updated_at DATETIME NOT NULL,
		expires_at DATETIME,
		PRIMARY KEY (chat_id, namespace, key)
	);`,
	`CREATE INDEX IF NOT EXISTS idx_assistant_group_state_expiry
		ON assistant_group_state(expires_at, chat_id, namespace, key);`,
}

func (MigrationProvider) Migrations() []database.Migration {
	return []database.Migration{migration001{}, migration002{}, migration003{}}
}

func (migration001) ID() string { return "assistant_group_state.001" }

func (migration001) Description() string {
	return "Durable revisioned Assistant group-scoped manager state"
}

func (migration001) Checksum() string {
	return "4963bf013a3370d9ba521c005c408dfbbe51064aeb6d976d68f2dd19cc2d4f5e"
}

func (migration001) LegacyVersions() []int { return nil }

func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	for _, statement := range schemaStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	for _, item := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: "assistant_group_state"},
		{kind: "index", name: "idx_assistant_group_state_expiry"},
	} {
		var count int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM sqlite_master
			WHERE type = ? AND name = ?
		`, item.kind, item.name).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("required %s %s does not exist", item.kind, item.name)
		}
	}
	return nil
}

var valueBoundStatements = []string{
	`CREATE TRIGGER IF NOT EXISTS trg_assistant_group_state_value_insert
		BEFORE INSERT ON assistant_group_state
		WHEN length(NEW.value) > 65536
		BEGIN
			SELECT RAISE(ABORT, 'assistant_group_state value exceeds 65536 bytes');
		END;`,
	`CREATE TRIGGER IF NOT EXISTS trg_assistant_group_state_value_update
		BEFORE UPDATE OF value ON assistant_group_state
		WHEN length(NEW.value) > 65536
		BEGIN
			SELECT RAISE(ABORT, 'assistant_group_state value exceeds 65536 bytes');
		END;`,
}

func (migration002) ID() string { return "assistant_group_state.002" }

func (migration002) Description() string {
	return "Enforce Assistant group-state value size at the database boundary"
}

func (migration002) Checksum() string {
	return "86faee8bde15fd594caf1ba67ae0aa53cad0de644686304398e8770d7ce8f077"
}

func (migration002) LegacyVersions() []int { return nil }

func (migration002) Up(ctx context.Context, tx database.SQLExecutor) error {
	for _, statement := range valueBoundStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (migration002) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	for _, trigger := range []string{
		"trg_assistant_group_state_value_insert",
		"trg_assistant_group_state_value_update",
	} {
		var count int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM sqlite_master
			WHERE type = 'trigger' AND name = ?
		`, trigger).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("required trigger %s does not exist", trigger)
		}
	}
	return nil
}


var coordinateBoundStatements = []string{
	`CREATE TRIGGER IF NOT EXISTS trg_assistant_group_state_coordinate_insert
		BEFORE INSERT ON assistant_group_state
		WHEN NEW.namespace = ''
		  OR NEW.key = ''
		  OR length(CAST(NEW.namespace AS BLOB)) > 64
		  OR length(CAST(NEW.key AS BLOB)) > 128
		  OR NEW.namespace <> trim(NEW.namespace)
		  OR NEW.key <> trim(NEW.key)
		  OR NEW.namespace <> lower(NEW.namespace)
		  OR NEW.key <> lower(NEW.key)
		  OR NEW.namespace GLOB '*[^a-z0-9._:/-]*'
		  OR NEW.key GLOB '*[^a-z0-9._:/-]*'
		BEGIN
			SELECT RAISE(ABORT, 'assistant_group_state coordinate is not canonical');
		END;`,
	`CREATE TRIGGER IF NOT EXISTS trg_assistant_group_state_coordinate_update
		BEFORE UPDATE OF namespace, key ON assistant_group_state
		WHEN NEW.namespace = ''
		  OR NEW.key = ''
		  OR length(CAST(NEW.namespace AS BLOB)) > 64
		  OR length(CAST(NEW.key AS BLOB)) > 128
		  OR NEW.namespace <> trim(NEW.namespace)
		  OR NEW.key <> trim(NEW.key)
		  OR NEW.namespace <> lower(NEW.namespace)
		  OR NEW.key <> lower(NEW.key)
		  OR NEW.namespace GLOB '*[^a-z0-9._:/-]*'
		  OR NEW.key GLOB '*[^a-z0-9._:/-]*'
		BEGIN
			SELECT RAISE(ABORT, 'assistant_group_state coordinate is not canonical');
		END;`,
}

func (migration003) ID() string { return "assistant_group_state.003" }

func (migration003) Description() string {
	return "Enforce canonical Assistant group-state coordinates"
}

func (migration003) Checksum() string {
	return "d73da4fabcc615ddd70ff19f673224a279f51a3d1faf32effcb731a4c16ee817"
}

func (migration003) LegacyVersions() []int { return nil }

func (migration003) Up(ctx context.Context, tx database.SQLExecutor) error {
	for _, statement := range coordinateBoundStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (migration003) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	for _, trigger := range []string{
		"trg_assistant_group_state_coordinate_insert",
		"trg_assistant_group_state_coordinate_update",
	} {
		var count int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM sqlite_master
			WHERE type = 'trigger' AND name = ?
		`, trigger).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("required trigger %s does not exist", trigger)
		}
	}
	return nil
}
