package clone

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
)

type migration001 struct{}

func (migration001) ID() string { return "clone.001" }
func (migration001) Description() string { return "Persistent clone profile snapshots" }
func (migration001) Checksum() string {
	return "44a893d59f5ae3be68967d0eb630b4dc1f4ce356b6d19e628eda614010c1500a"
}
func (migration001) LegacyVersions() []int { return []int{15} }
func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS clone_state (
			owner_id INTEGER PRIMARY KEY,
			original_first_name TEXT NOT NULL DEFAULT '',
			original_last_name TEXT NOT NULL DEFAULT '',
			original_bio TEXT NOT NULL DEFAULT '',
			original_photo_path TEXT NOT NULL DEFAULT '',
			active BOOLEAN NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL
		);`)
	return err
}

type migration002 struct{}

func (migration002) ID() string { return "clone.002" }
func (migration002) Description() string { return "Clone snapshot photo mutation tracking" }
func (migration002) Checksum() string {
	return "91fd1d917cf3673d4b2cf720800d457c82e360d9ddbd6bde5d053b88b3e2069d"
}
func (migration002) LegacyVersions() []int { return []int{16} }
func (migration002) Up(ctx context.Context, tx database.SQLExecutor) error {
	_, err := tx.ExecContext(ctx, `
		ALTER TABLE clone_state ADD COLUMN cloned_photo BOOLEAN NOT NULL DEFAULT 0;`)
	return err
}

func Migrations() []database.Migration {
	return []database.Migration{migration001{}, migration002{}}
}
