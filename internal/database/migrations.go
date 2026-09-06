package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	{
		version:     7,
		description: "Persistent peer entities and local username cache",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS peers_entities (
				prefix TEXT NOT NULL,
				id INTEGER NOT NULL,
				username TEXT NOT NULL DEFAULT '',
				phone TEXT NOT NULL DEFAULT '',
				first_name TEXT NOT NULL DEFAULT '',
				last_name TEXT NOT NULL DEFAULT '',
				title TEXT NOT NULL DEFAULT '',
				updated_at DATETIME NOT NULL,
				PRIMARY KEY (prefix, id)
			);`,
			`CREATE INDEX IF NOT EXISTS idx_peers_entities_username ON peers_entities(username COLLATE NOCASE);`,
		},
	},
	{
		version:     8,
		description: "Persistent moderation warnings and infraction records",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS moderation_warnings (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				chat_id INTEGER NOT NULL,
				user_id INTEGER NOT NULL,
				reason TEXT NOT NULL DEFAULT '',
				warned_by INTEGER NOT NULL,
				created_at DATETIME NOT NULL
			);`,
			`CREATE INDEX IF NOT EXISTS idx_moderation_warnings_chat_user ON moderation_warnings(chat_id, user_id);`,
		},
	},
	{
		version:     9,
		description: "PM permit security records and user log settings",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS pm_permit_records (
				user_id INTEGER PRIMARY KEY,
				status TEXT NOT NULL,
				first_seen_at DATETIME NOT NULL,
				last_seen_at DATETIME NOT NULL,
				expires_at DATETIME,
				reason TEXT NOT NULL DEFAULT '',
				warn_count INTEGER NOT NULL DEFAULT 0
			);`,
			`CREATE INDEX IF NOT EXISTS idx_pm_permit_status ON pm_permit_records(status);`,
			`CREATE TABLE IF NOT EXISTS user_log_settings (
				key TEXT PRIMARY KEY,
				val TEXT NOT NULL
			);`,
		},
	},
	{
		version:     10,
		description: "Voice chat sessions and playback queue",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS voice_sessions (
				chat_id INTEGER PRIMARY KEY,
				state TEXT NOT NULL,
				volume INTEGER NOT NULL DEFAULT 100,
				repeat_mode TEXT NOT NULL DEFAULT 'off',
				updated_at DATETIME NOT NULL
			);`,
			`CREATE TABLE IF NOT EXISTS voice_queue (
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
			);`,
			`CREATE INDEX IF NOT EXISTS idx_voice_queue_chat_pos ON voice_queue(chat_id, position);`,
		},
	},
	{
		version:     11,
		description: "Addon registry for external plugin ecosystem",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS addon_registry (
				name TEXT PRIMARY KEY,
				version TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				author TEXT NOT NULL DEFAULT '',
				source_url TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL DEFAULT 'active',
				capabilities TEXT NOT NULL DEFAULT '',
				min_version TEXT NOT NULL DEFAULT '',
				installed_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			);`,
			`CREATE INDEX IF NOT EXISTS idx_addon_registry_status ON addon_registry(status);`,
		},
	},
	{
		version:     12,
		description: "PM permit warn message tracking for delete-after-approve",
		statements: []string{
			`ALTER TABLE pm_permit_records ADD COLUMN warn_msg_ids TEXT NOT NULL DEFAULT '[]';`,
		},
	},
}

// calculateMigrationChecksum produces a deterministic SHA-256 hash of a migration's SQL statements.
func calculateMigrationChecksum(m migration) string {
	h := sha256.New()
	for _, s := range m.statements {
		h.Write([]byte(strings.TrimSpace(s)))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// migrate runs pending database migrations in sequence inside atomic transactions.
func (d *DB) migrate(ctx context.Context) error {
	// 1. Ensure schema_migrations table exists with checksum column
	createMigrationsTable := `
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		description TEXT NOT NULL,
		checksum TEXT NOT NULL DEFAULT '',
		applied_at DATETIME NOT NULL
	);`
	if _, err := d.ExecContext(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}

	// Backward compatibility: ensure checksum column exists on older schema_migrations
	var colCount int
	_ = d.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('schema_migrations') WHERE name='checksum'").Scan(&colCount)
	if colCount == 0 {
		_, _ = d.ExecContext(ctx, "ALTER TABLE schema_migrations ADD COLUMN checksum TEXT NOT NULL DEFAULT ''")
	}

	// 2. Load already applied versions and checksums
	rows, err := d.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version ASC")
	if err != nil {
		return fmt.Errorf("failed to query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]string)
	for rows.Next() {
		var v int
		var cs string
		if err := rows.Scan(&v, &cs); err != nil {
			return fmt.Errorf("failed to scan migration version: %w", err)
		}
		applied[v] = cs
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
			cs1 := calculateMigrationChecksum(migrations[0])
			_, err := d.ExecContext(ctx, "INSERT INTO schema_migrations (version, description, checksum, applied_at) VALUES (1, 'Legacy schema adoption', ?, ?)", cs1, time.Now())
			if err == nil {
				applied[1] = cs1
			}
		}
	}

	// 4. Apply pending migrations sequentially and verify checksums of existing
	for _, m := range migrations {
		expectedChecksum := calculateMigrationChecksum(m)
		if savedChecksum, exists := applied[m.version]; exists {
			// If a checksum was recorded, verify it hasn't been altered
			if savedChecksum != "" && savedChecksum != expectedChecksum {
				return fmt.Errorf("migration checksum mismatch for version %d (%s): recorded %s, calculated %s",
					m.version, m.description, savedChecksum, expectedChecksum)
			}
			// Backfill checksum if it was empty from legacy schema
			if savedChecksum == "" {
				_, _ = d.ExecContext(ctx, "UPDATE schema_migrations SET checksum = ? WHERE version = ?", expectedChecksum, m.version)
			}
			continue
		}

		if err := d.applyMigration(ctx, m, expectedChecksum); err != nil {
			return fmt.Errorf("failed to apply migration version %d (%s): %w", m.version, m.description, err)
		}
	}

	return nil
}

// applyMigration applies a single migration step within a transaction.
func (d *DB) applyMigration(ctx context.Context, m migration, checksum string) error {
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

	// Record migration completion with checksum
	recordQuery := "INSERT INTO schema_migrations (version, description, checksum, applied_at) VALUES (?, ?, ?, ?)"
	if _, err := tx.ExecContext(ctx, recordQuery, m.version, m.description, checksum, time.Now()); err != nil {
		return fmt.Errorf("failed to record migration version %d: %w", m.version, err)
	}

	return tx.Commit()
}

// columnExists checks whether a column already exists in a given table.
func (d *DB) columnExists(ctx context.Context, tx *sql.Tx, tableName, columnName string) bool {
	validTables := map[string]bool{
		"sudo_users":            true,
		"notes":                 true,
		"afk_status":            true,
		"filters":               true,
		"scheduled_jobs":        true,
		"blacklists":            true,
		"peers_storage":         true,
		"peers_phones":          true,
		"peers_metadata":        true,
		"peers_entities":        true,
		"scheduled_job_history": true,
		"moderation_warnings":   true,
		"pm_permit_records":     true,
		"addon_registry":        true,
	}
	if !validTables[strings.ToLower(tableName)] {
		return false
	}

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
