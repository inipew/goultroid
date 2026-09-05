package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps a sql.DB connection with custom repository methods.
type DB struct {
	*sql.DB
}

// Open initializes SQLite database at the specified path and runs initial migrations.
// If dsn is ":memory:", an in-memory database will be used (useful for tests).
func Open(dsn string) (*DB, error) {
	if dsn == "" {
		dsn = "data/goultroid.db"
	}

	if dsn != ":memory:" {
		dir := filepath.Dir(dsn)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create db directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Configure pool for SQLite
	db.SetMaxOpenConns(1) // SQLite works best with single writer or WAL mode
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)

	// Set pragmas
	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA foreign_keys = ON;",
		"PRAGMA synchronous = NORMAL;",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("failed to set pragma %q: %w", pragma, err)
		}
	}

	instance := &DB{DB: db}
	if err := instance.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migration failed: %w", err)
	}

	return instance, nil
}

func (d *DB) migrate(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS sudo_users (
		user_id INTEGER PRIMARY KEY,
		added_at DATETIME NOT NULL,
		added_by INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS notes (
		chat_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		content TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (chat_id, name)
	);

	CREATE TABLE IF NOT EXISTS afk_status (
		user_id INTEGER PRIMARY KEY,
		is_afk BOOLEAN NOT NULL DEFAULT 0,
		reason TEXT NOT NULL DEFAULT '',
		since DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS filters (
		chat_id INTEGER NOT NULL,
		keyword TEXT NOT NULL,
		reply_text TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		PRIMARY KEY (chat_id, keyword)
	);
	`
	_, err := d.ExecContext(ctx, schema)
	return err
}
