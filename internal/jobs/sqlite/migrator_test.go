package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
)

func setupLegacyAndNewDB(t *testing.T) (*database.DB, *jobsqlite.Migrator) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}

	ctx := context.Background()

	// Initialize legacy scheduled_jobs schema
	legacySchema := `
	CREATE TABLE IF NOT EXISTS scheduled_jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id INTEGER NOT NULL,
		peer_type TEXT NOT NULL DEFAULT 'chat',
		access_hash INTEGER NOT NULL DEFAULT 0,
		action_type TEXT NOT NULL,
		payload TEXT NOT NULL,
		interval_seconds INTEGER DEFAULT 0,
		next_run_at DATETIME NOT NULL,
		created_at DATETIME NOT NULL,
		created_by INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NOT NULL DEFAULT '',
		attempt_count INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'pending',
		max_attempts INTEGER NOT NULL DEFAULT 3,
		lease_until DATETIME,
		claimed_at DATETIME,
		last_started_at DATETIME,
		last_finished_at DATETIME,
		claim_token TEXT NOT NULL DEFAULT ''
	);`
	if _, err := db.DB.ExecContext(ctx, legacySchema); err != nil {
		t.Fatalf("init legacy schema: %v", err)
	}

	// Initialize redesigned job schema
	if err := jobsqlite.InitSchema(ctx, db.DB); err != nil {
		t.Fatalf("init redesigned schema: %v", err)
	}

	return db, jobsqlite.NewMigrator(db.DB)
}

func insertLegacyJob(t *testing.T, db *sql.DB, id int64, actionType, payload string, interval int, nextRun time.Time, status string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO scheduled_jobs (
			id, chat_id, peer_type, access_hash, action_type, payload,
			interval_seconds, next_run_at, created_at, created_by, status
		) VALUES (?, 12345, 'channel', 67890, ?, ?, ?, ?, ?, 999, ?)
	`, id, actionType, payload, interval, nextRun, time.Now().UTC(), status)
	if err != nil {
		t.Fatalf("insert legacy job %d: %v", id, err)
	}
}

func TestMigrator_DryRunAndBlockedRecords(t *testing.T) {
	db, migrator := setupLegacyAndNewDB(t)
	defer db.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	// Insert 1 valid message job
	insertLegacyJob(t, db.DB, 1, "message", "hello world", 60, now.Add(time.Minute), "pending")
	// Insert 1 valid command job
	insertLegacyJob(t, db.DB, 2, "command", ".ping", 0, now.Add(-time.Minute), "pending")
	// Insert 1 blocked job with empty payload
	insertLegacyJob(t, db.DB, 3, "message", "", 120, now.Add(time.Hour), "pending")
	// Insert 1 blocked job with invalid action_type
	insertLegacyJob(t, db.DB, 4, "invalid_action", "some data", 0, now.Add(time.Hour), "pending")

	report, err := migrator.DryRun(ctx)
	if err != nil {
		t.Fatalf("DryRun error: %v", err)
	}

	if report.TotalLegacyRows != 4 {
		t.Errorf("expected 4 legacy rows, got %d", report.TotalLegacyRows)
	}
	if report.MappedCount != 2 {
		t.Errorf("expected 2 mapped rows, got %d", report.MappedCount)
	}
	if report.BlockedCount != 2 {
		t.Errorf("expected 2 blocked rows, got %d", report.BlockedCount)
	}
	if report.DueBacklogCount != 1 {
		t.Errorf("expected 1 due backlog row, got %d", report.DueBacklogCount)
	}

	// Verify database is completely untouched by DryRun
	var defCount, schedCount, mapCount int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM job_definitions`).Scan(&defCount)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM job_schedules`).Scan(&schedCount)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM job_migration_map`).Scan(&mapCount)
	if defCount != 0 || schedCount != 0 || mapCount != 0 {
		t.Errorf("DryRun modified database: defs=%d, scheds=%d, maps=%d", defCount, schedCount, mapCount)
	}
}

func TestMigrator_FullTransactionalMigrationAndIdempotency(t *testing.T) {
	db, migrator := setupLegacyAndNewDB(t)
	defer db.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	insertLegacyJob(t, db.DB, 10, "message", "recurring ping", 300, now.Add(5*time.Minute), "pending")
	insertLegacyJob(t, db.DB, 20, "command", ".alive", 0, now.Add(time.Minute), "paused")

	// 1. Run first migration
	report, err := migrator.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate error: %v", err)
	}

	if report.MappedCount != 2 {
		t.Fatalf("expected 2 mapped, got %d", report.MappedCount)
	}

	// Verify job_definitions, job_schedules, and job_migration_map
	var defCount, schedCount, mapCount int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM job_definitions`).Scan(&defCount)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM job_schedules`).Scan(&schedCount)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM job_migration_map`).Scan(&mapCount)
	if defCount != 2 || schedCount != 2 || mapCount != 2 {
		t.Errorf("mismatch after migrate: defs=%d, scheds=%d, maps=%d", defCount, schedCount, mapCount)
	}

	// Verify enabled state on paused job
	var enabled int
	err = db.DB.QueryRow(`SELECT enabled FROM job_schedules WHERE id = 'sched:scheduled:20'`).Scan(&enabled)
	if err != nil || enabled != 0 {
		t.Errorf("expected paused job schedule to have enabled=0, got %d, err=%v", enabled, err)
	}

	// 2. Run second migration to verify Idempotency
	report2, err := migrator.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate error: %v", err)
	}
	if report2.SkippedCount != 2 {
		t.Errorf("expected 2 skipped on second run, got %d", report2.SkippedCount)
	}
	if report2.MappedCount != 0 {
		t.Errorf("expected 0 mapped on second run, got %d", report2.MappedCount)
	}
}

func TestMigrator_DeltaValidationAndReverseProjection(t *testing.T) {
	db, migrator := setupLegacyAndNewDB(t)
	defer db.Close()

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	insertLegacyJob(t, db.DB, 100, "message", "test msg", 60, now, "pending")
	insertLegacyJob(t, db.DB, 200, "command", "test cmd", 120, now.Add(time.Hour), "pending")

	_, err := migrator.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// Validate delta
	delta, err := migrator.ValidateDelta(ctx)
	if err != nil {
		t.Fatalf("ValidateDelta failed: %v", err)
	}
	if !delta.ChecksumMatch {
		t.Errorf("expected ChecksumMatch=true, got false. Discrepancies: %v", delta.Discrepancies)
	}
	if delta.LegacyRowCount != 2 || delta.NewDefCount != 2 || delta.NewSchedCount != 2 {
		t.Errorf("unexpected counts: %+v", delta)
	}

	// Simulate schedule advance in new schema
	futureTime := now.Add(24 * time.Hour)
	_, err = db.DB.Exec(`UPDATE job_schedules SET next_due_at = ?, enabled = 0 WHERE id = 'sched:scheduled:100'`, futureTime)
	if err != nil {
		t.Fatalf("advance new schedule: %v", err)
	}

	// Run ReverseProject for rollback safety
	if err := migrator.ReverseProject(ctx); err != nil {
		t.Fatalf("ReverseProject failed: %v", err)
	}

	// Verify legacy scheduled_jobs is updated
	var updatedNextRun time.Time
	var updatedStatus string
	err = db.DB.QueryRow(`SELECT next_run_at, status FROM scheduled_jobs WHERE id = 100`).Scan(&updatedNextRun, &updatedStatus)
	if err != nil {
		t.Fatalf("query updated legacy row: %v", err)
	}
	if updatedStatus != "paused" {
		t.Errorf("expected status 'paused', got %q", updatedStatus)
	}
	if !updatedNextRun.Equal(futureTime) {
		t.Errorf("expected next_run_at %v, got %v", futureTime, updatedNextRun)
	}
}
