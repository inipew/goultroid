package notes

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

var (
	_ database.SchemaInvariantMigration = migration001{}
	_ database.SchemaInvariantMigration = migration002{}
	_ database.SchemaInvariantMigration = migration003{}
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
func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='notes'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required table notes does not exist")
	}
	return nil
}

type migration002 struct{}

func (migration002) ID() string          { return "notes.002" }
func (migration002) Description() string { return "Rich saved response metadata for notes" }
func (migration002) Checksum() string {
	return "36ce39b45c7efb867fd4ad1770e677df1bdfaf02a3681b515167aec948d7095d"
}
func (migration002) LegacyVersions() []int { return nil }
func (migration002) Up(ctx context.Context, tx database.SQLExecutor) error {
	for _, statement := range []string{
		`ALTER TABLE notes ADD COLUMN response_format TEXT NOT NULL DEFAULT 'html';`,
		`ALTER TABLE notes ADD COLUMN media_asset_id TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE notes ADD COLUMN media_type TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE notes ADD COLUMN media_name TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE notes ADD COLUMN media_mime TEXT NOT NULL DEFAULT '';`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
func (migration002) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	for _, column := range []string{"response_format", "media_asset_id", "media_type", "media_name", "media_mime"} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('notes') WHERE name = ?`, column).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("required column %s in table notes does not exist", column)
		}
	}
	return nil
}

type migration003 struct{}

func (migration003) ID() string          { return "notes.003" }
func (migration003) Description() string { return "Index persistent media references for bounded reconciliation" }
func (migration003) Checksum() string {
	return "760eee853a98e0046d43166cc8f96cd5d87113b3f32445c82356f8ece258bf5b"
}
func (migration003) LegacyVersions() []int { return nil }
func (migration003) Up(ctx context.Context, tx database.SQLExecutor) error {
	_, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_notes_media_asset_id_trim ON notes(TRIM(media_asset_id));`)
	return err
}
func (migration003) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_notes_media_asset_id_trim'
	`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required index idx_notes_media_asset_id_trim does not exist")
	}
	return nil
}

func Migrations() []database.Migration {
	return []database.Migration{migration001{}, migration002{}, migration003{}}
}
