package filters

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
func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='filters'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required table filters does not exist")
	}
	return nil
}

type migration002 struct{}

func (migration002) ID() string          { return "filters.002" }
func (migration002) Description() string { return "Rich saved response metadata for filters" }
func (migration002) Checksum() string {
	return "bf16dd3ba62191d1fcf4f19267ab0266c67590a8221f3ed84ffb51f6a4dde0bc"
}
func (migration002) LegacyVersions() []int { return nil }
func (migration002) Up(ctx context.Context, tx database.SQLExecutor) error {
	for _, statement := range []string{
		`ALTER TABLE filters ADD COLUMN response_format TEXT NOT NULL DEFAULT 'html';`,
		`ALTER TABLE filters ADD COLUMN media_asset_id TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE filters ADD COLUMN media_type TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE filters ADD COLUMN media_name TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE filters ADD COLUMN media_mime TEXT NOT NULL DEFAULT '';`,
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
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('filters') WHERE name = ?`, column).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("required column %s in table filters does not exist", column)
		}
	}
	return nil
}

type migration003 struct{}

func (migration003) ID() string          { return "filters.003" }
func (migration003) Description() string { return "Index persistent media references for bounded reconciliation" }
func (migration003) Checksum() string {
	return "0cb39daebeb0716fa820ac5b6ab29fe9e1570b7c41981db96a8f893e9a17df84"
}
func (migration003) LegacyVersions() []int { return nil }
func (migration003) Up(ctx context.Context, tx database.SQLExecutor) error {
	_, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_filters_media_asset_id_trim ON filters(TRIM(media_asset_id));`)
	return err
}
func (migration003) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_filters_media_asset_id_trim'
	`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required index idx_filters_media_asset_id_trim does not exist")
	}
	return nil
}

func Migrations() []database.Migration {
	return []database.Migration{migration001{}, migration002{}, migration003{}}
}
