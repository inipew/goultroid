package afk

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

type migration001 struct{}

func (migration001) ID() string          { return "afk.001" }
func (migration001) Description() string { return "AFK user status and reason storage" }
func (migration001) Checksum() string {
	return "f4d9c7921ba0999518d6a8ad52c78a05c3121516f456108b5e9858597fbb24a2"
}
func (migration001) LegacyVersions() []int { return []int{1} }
func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS afk_status (
			user_id INTEGER PRIMARY KEY,
			is_afk BOOLEAN NOT NULL DEFAULT 0,
			reason TEXT NOT NULL DEFAULT '',
			since DATETIME NOT NULL
		);`)
	return err
}

func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='afk_status'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required table afk_status does not exist")
	}
	return nil
}

func Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}
