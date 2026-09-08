package voice

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

var _ database.SchemaInvariantMigration = migration001{}

type migration001 struct{}

func (migration001) ID() string          { return "voice.001" }
func (migration001) Description() string { return "Voice chat sessions and playback queue" }
func (migration001) Checksum() string {
	return "8d1202a02268f8ff78c865ab184478914bc5b7d7a1a5c12667a25cbad68233ed"
}
func (migration001) LegacyVersions() []int { return []int{10} }

func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	query := `CREATE TABLE IF NOT EXISTS voice_sessions (
	chat_id INTEGER PRIMARY KEY,
	state TEXT NOT NULL,
	volume INTEGER NOT NULL DEFAULT 100,
	repeat_mode TEXT NOT NULL DEFAULT 'off',
	updated_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS voice_queue (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id INTEGER NOT NULL,
	position INTEGER NOT NULL,
	title TEXT NOT NULL,
	artist TEXT NOT NULL DEFAULT '',
	source_url TEXT NOT NULL DEFAULT '',
	file_path TEXT NOT NULL DEFAULT '',
	duration_seconds INTEGER NOT NULL DEFAULT 0,
	source_type TEXT NOT NULL DEFAULT 'audio',
	requester_id INTEGER NOT NULL,
	created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_voice_queue_chat_pos ON voice_queue(chat_id, position);`

	_, err := tx.ExecContext(ctx, query)
	return err
}

func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var tableCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='voice_sessions'`).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount == 0 {
		return fmt.Errorf("required table voice_sessions does not exist")
	}

	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='voice_queue'`).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount == 0 {
		return fmt.Errorf("required table voice_queue does not exist")
	}

	var colCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('voice_sessions') WHERE name='volume'`).Scan(&colCount); err != nil {
		return err
	}
	if colCount == 0 {
		return fmt.Errorf("required column volume in table voice_sessions does not exist")
	}

	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('voice_queue') WHERE name='duration_seconds'`).Scan(&colCount); err != nil {
		return err
	}
	if colCount == 0 {
		return fmt.Errorf("required column duration_seconds in table voice_queue does not exist")
	}

	return nil
}

func Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}
