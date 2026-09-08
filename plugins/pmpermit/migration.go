package pmpermit

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

var _ database.SchemaInvariantMigration = migration001{}

type migration001 struct{}

func (migration001) ID() string          { return "pmpermit.001" }
func (migration001) Description() string { return "Persistent PM permit records and warnings" }
func (migration001) Checksum() string {
	return "5b98ef1a72dca849f78eb48c081971dbe5982161b9b9409849202a0aef84b39b"
}
func (migration001) LegacyVersions() []int { return []int{4, 7} }

func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	query := `
		CREATE TABLE IF NOT EXISTS pm_permit_records (
			user_id INTEGER PRIMARY KEY,
			status TEXT NOT NULL,
			first_seen_at TIMESTAMP NOT NULL,
			last_seen_at TIMESTAMP NOT NULL,
			expires_at TIMESTAMP,
			reason TEXT,
			warn_count INTEGER NOT NULL DEFAULT 0,
			warn_msg_ids TEXT NOT NULL DEFAULT '[]'
		);
		CREATE INDEX IF NOT EXISTS idx_pm_permit_status ON pm_permit_records(status);
	`
	_, err := tx.ExecContext(ctx, query)
	return err
}

func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var tableCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='pm_permit_records'`).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount == 0 {
		return fmt.Errorf("required table pm_permit_records does not exist")
	}

	var colCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('pm_permit_records') WHERE name='warn_msg_ids'`).Scan(&colCount); err != nil {
		return err
	}
	if colCount == 0 {
		return fmt.Errorf("required column warn_msg_ids in table pm_permit_records does not exist")
	}
	return nil
}

func Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}
