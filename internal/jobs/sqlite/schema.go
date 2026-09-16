package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// InitSchema creates the redesigned durable jobs tables, indexes, and foreign keys (ADR 0006 §7.1).
func InitSchema(ctx context.Context, db *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS execution_runtime_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			mode TEXT NOT NULL,
			generation INTEGER NOT NULL,
			updated_at DATETIME NOT NULL
		);`,
		`INSERT OR IGNORE INTO execution_runtime_state (id, mode, generation, updated_at)
		 VALUES (1, 'legacy', 0, CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS job_definitions (
			id TEXT PRIMARY KEY,
			scope_owner TEXT NOT NULL,
			quota_owner TEXT NOT NULL,
			handler_type TEXT NOT NULL,
			version INTEGER NOT NULL DEFAULT 1,
			payload BLOB,
			pool TEXT NOT NULL DEFAULT 'general',
			class TEXT NOT NULL DEFAULT 'normal',
			timeout_ms INTEGER NOT NULL DEFAULT 0,
			retry_policy TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			revision INTEGER NOT NULL DEFAULT 1,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_job_defs_scope ON job_definitions(scope_owner);`,
		`CREATE INDEX IF NOT EXISTS idx_job_defs_quota ON job_definitions(quota_owner);`,

		`CREATE TABLE IF NOT EXISTS job_schedules (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL REFERENCES job_definitions(id) ON DELETE CASCADE,
			recurrence TEXT NOT NULL,
			interval_seconds INTEGER NOT NULL DEFAULT 0,
			timezone TEXT NOT NULL DEFAULT 'UTC',
			next_due_at DATETIME NOT NULL,
			misfire_policy TEXT NOT NULL DEFAULT 'run_once',
			overlap_policy TEXT NOT NULL DEFAULT 'forbid',
			enabled INTEGER NOT NULL DEFAULT 1,
			revision INTEGER NOT NULL DEFAULT 1,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_job_sched_next ON job_schedules(enabled, next_due_at);`,

		`CREATE TABLE IF NOT EXISTS job_occurrences (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL REFERENCES job_definitions(id) ON DELETE CASCADE,
			schedule_id TEXT REFERENCES job_schedules(id) ON DELETE SET NULL,
			scheduled_for DATETIME NOT NULL,
			occurrence_key TEXT NOT NULL UNIQUE,
			state TEXT NOT NULL DEFAULT 'ready',
			ready_at DATETIME NOT NULL,
			cancel_epoch INTEGER NOT NULL DEFAULT 0,
			revision INTEGER NOT NULL DEFAULT 1,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_job_occ_state ON job_occurrences(state, ready_at);`,
		`CREATE INDEX IF NOT EXISTS idx_job_occ_job ON job_occurrences(job_id);`,

		`CREATE TABLE IF NOT EXISTS job_attempts (
			id TEXT PRIMARY KEY,
			occurrence_id TEXT NOT NULL REFERENCES job_occurrences(id) ON DELETE CASCADE,
			attempt_no INTEGER NOT NULL,
			task_id TEXT NOT NULL UNIQUE,
			lease_epoch INTEGER NOT NULL,
			lease_until DATETIME NOT NULL,
			state TEXT NOT NULL,
			started_at DATETIME,
			finished_at DATETIME,
			result BLOB,
			error TEXT,
			created_at DATETIME NOT NULL,
			UNIQUE(occurrence_id, attempt_no)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_job_attempts_lease ON job_attempts(state, lease_until);`,

		`CREATE TABLE IF NOT EXISTS job_outbox (
			event_id TEXT PRIMARY KEY,
			occurrence_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			payload BLOB,
			committed_at DATETIME NOT NULL,
			delivery_state TEXT NOT NULL DEFAULT 'pending'
		);`,
		`CREATE INDEX IF NOT EXISTS idx_job_outbox_undelivered ON job_outbox(delivery_state, committed_at);`,

		`CREATE TABLE IF NOT EXISTS job_migration_map (
			legacy_domain TEXT NOT NULL,
			legacy_id TEXT NOT NULL,
			new_job_id TEXT NOT NULL,
			new_schedule_id TEXT,
			new_occurrence_id TEXT,
			migration_revision INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY (legacy_domain, legacy_id, migration_revision)
		);`,
	}

	for _, query := range queries {
		if _, err := db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("init job schema query failed: %w", err)
		}
	}
	return nil
}
