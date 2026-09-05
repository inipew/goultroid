package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// migration defines a versioned schema update step.
type migration struct {
	version     int
	description string
	statements  []string
}

// migrations contains the chronological list of schema updates.
var migrations = []migration{
	{
		version:     1,
		description: "Initial schema creation",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS sudo_users (
				user_id INTEGER PRIMARY KEY,
				added_at DATETIME NOT NULL,
				added_by INTEGER NOT NULL
			);`,
			`CREATE TABLE IF NOT EXISTS notes (
				chat_id INTEGER NOT NULL,
				name TEXT NOT NULL,
				content TEXT NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				PRIMARY KEY (chat_id, name)
			);`,
			`CREATE TABLE IF NOT EXISTS afk_status (
				user_id INTEGER PRIMARY KEY,
				is_afk BOOLEAN NOT NULL DEFAULT 0,
				reason TEXT NOT NULL DEFAULT '',
				since DATETIME NOT NULL
			);`,
			`CREATE TABLE IF NOT EXISTS filters (
				chat_id INTEGER NOT NULL,
				keyword TEXT NOT NULL,
				reply_text TEXT NOT NULL,
				created_at DATETIME NOT NULL,
				PRIMARY KEY (chat_id, keyword)
			);`,
			`CREATE TABLE IF NOT EXISTS scheduled_jobs (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				chat_id INTEGER NOT NULL,
				peer_type TEXT NOT NULL DEFAULT 'chat',
				access_hash INTEGER NOT NULL DEFAULT 0,
				action_type TEXT NOT NULL,
				payload TEXT NOT NULL,
				interval_seconds INTEGER DEFAULT 0,
				next_run_at DATETIME NOT NULL,
				created_at DATETIME NOT NULL
			);`,
			`CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_next_run ON scheduled_jobs(next_run_at);`,
			`CREATE TABLE IF NOT EXISTS blacklists (
				chat_id INTEGER NOT NULL,
				word TEXT NOT NULL,
				created_at DATETIME NOT NULL,
				PRIMARY KEY (chat_id, word)
			);`,
		},
	},
	{
		version:     2,
		description: "Scheduler durable state: principal, error tracking, and indices",
		statements: []string{
			`ALTER TABLE scheduled_jobs ADD COLUMN created_by INTEGER NOT NULL DEFAULT 0;`,
			`ALTER TABLE scheduled_jobs ADD COLUMN last_error TEXT NOT NULL DEFAULT '';`,
			`ALTER TABLE scheduled_jobs ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0;`,
			`CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_chat_id ON scheduled_jobs(chat_id);`,
		},
	},
	{
		version:     3,
		description: "Scheduler distributed lease, claim state, and retry machine",
		statements: []string{
			`ALTER TABLE scheduled_jobs ADD COLUMN status TEXT NOT NULL DEFAULT 'pending';`,
			`ALTER TABLE scheduled_jobs ADD COLUMN max_attempts INTEGER NOT NULL DEFAULT 3;`,
			`ALTER TABLE scheduled_jobs ADD COLUMN lease_until DATETIME;`,
			`ALTER TABLE scheduled_jobs ADD COLUMN claimed_at DATETIME;`,
			`ALTER TABLE scheduled_jobs ADD COLUMN last_started_at DATETIME;`,
			`ALTER TABLE scheduled_jobs ADD COLUMN last_finished_at DATETIME;`,
			`CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_claim ON scheduled_jobs(status, next_run_at, lease_until);`,
		},
	},
	{
		version:     4,
		description: "Scheduler fencing tokens and high-performance due index",
		statements: []string{
			`ALTER TABLE scheduled_jobs ADD COLUMN claim_token TEXT NOT NULL DEFAULT '';`,
			`CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_due ON scheduled_jobs(next_run_at, status, lease_until);`,
			`CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_claim_token ON scheduled_jobs(claim_token);`,
		},
	},
	{
		version:     5,
		description: "Persistent peer storage for access hash caching",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS peers_storage (
				prefix TEXT NOT NULL,
				id INTEGER NOT NULL,
				access_hash INTEGER NOT NULL,
				updated_at DATETIME NOT NULL,
				PRIMARY KEY (prefix, id)
			);`,
			`CREATE TABLE IF NOT EXISTS peers_phones (
				phone TEXT PRIMARY KEY,
				prefix TEXT NOT NULL,
				id INTEGER NOT NULL,
				access_hash INTEGER NOT NULL,
				updated_at DATETIME NOT NULL
			);`,
			`CREATE TABLE IF NOT EXISTS peers_metadata (
				key TEXT PRIMARY KEY,
				int_val INTEGER NOT NULL
			);`,
		},
	},
	{
		version:     6,
		description: "Scheduler execution history for audit trail",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS scheduled_job_history (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				job_id INTEGER NOT NULL,
				ran_at DATETIME NOT NULL,
				duration_ms INTEGER NOT NULL DEFAULT 0,
				success BOOLEAN NOT NULL DEFAULT 0,
				error_msg TEXT NOT NULL DEFAULT ''
			);`,
			`CREATE INDEX IF NOT EXISTS idx_job_history_job_id ON scheduled_job_history(job_id);`,
			`CREATE INDEX IF NOT EXISTS idx_job_history_ran_at ON scheduled_job_history(ran_at DESC);`,
		},
	},
}

// migrate runs pending database migrations in sequence inside atomic transactions.
func (d *DB) migrate(ctx context.Context) error {
	// 1. Ensure schema_migrations table exists
	createMigrationsTable := `
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		description TEXT NOT NULL,
		applied_at DATETIME NOT NULL
	);`
	if _, err := d.ExecContext(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}

	// 2. Load already applied versions
	rows, err := d.QueryContext(ctx, "SELECT version FROM schema_migrations ORDER BY version ASC")
	if err != nil {
		return fmt.Errorf("failed to query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return fmt.Errorf("failed to scan migration version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// 3. Backward-compatibility adoption for unversioned databases:
	// If schema_migrations is empty, check if legacy tables already exist.
	if len(applied) == 0 {
		var tableName string
		err := d.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name='sudo_users'").Scan(&tableName)
		if err == nil && tableName == "sudo_users" {
			// Legacy unversioned DB detected: adopt as version 1
			_, err := d.ExecContext(ctx, "INSERT INTO schema_migrations (version, description, applied_at) VALUES (1, 'Legacy schema adoption', ?)", time.Now())
			if err == nil {
				applied[1] = true
			}
		}
	}

	// 4. Apply pending migrations sequentially
	for _, m := range migrations {
		if applied[m.version] {
			continue
		}

		if err := d.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("failed to apply migration version %d (%s): %w", m.version, m.description, err)
		}
	}

	return nil
}

// applyMigration applies a single migration step within a transaction.
func (d *DB) applyMigration(ctx context.Context, m migration) error {
	tx, err := d.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for _, stmt := range m.statements {
		trimmed := strings.TrimSpace(stmt)
		if trimmed == "" {
			continue
		}
		// Check for idempotent column addition if column might already exist
		if strings.HasPrefix(strings.ToUpper(trimmed), "ALTER TABLE ") && strings.Contains(strings.ToUpper(trimmed), " ADD COLUMN ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 6 {
				tbl := parts[2]
				col := parts[5]
				if d.columnExists(ctx, tx, tbl, col) {
					continue
				}
			}
		}

		if _, err := tx.ExecContext(ctx, trimmed); err != nil {
			return fmt.Errorf("error executing statement %q: %w", trimmed, err)
		}
	}

	// Record migration completion
	recordQuery := "INSERT INTO schema_migrations (version, description, applied_at) VALUES (?, ?, ?)"
	if _, err := tx.ExecContext(ctx, recordQuery, m.version, m.description, time.Now()); err != nil {
		return fmt.Errorf("failed to record migration version %d: %w", m.version, err)
	}

	return tx.Commit()
}

// columnExists checks whether a column already exists in a given table.
func (d *DB) columnExists(ctx context.Context, tx *sql.Tx, tableName, columnName string) bool {
	query := fmt.Sprintf("PRAGMA table_info(%s)", tableName)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return false
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var dfltValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err == nil {
			if strings.EqualFold(name, columnName) {
				return true
			}
		}
	}
	return false
}
