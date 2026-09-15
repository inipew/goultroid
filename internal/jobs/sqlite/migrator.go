package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MigrationStatus tracks the disposition of a migrated legacy record.
type MigrationStatus string

const (
	StatusSuccess MigrationStatus = "success"
	StatusBlocked MigrationStatus = "blocked"
	StatusSkipped MigrationStatus = "skipped"
)

// LegacyJobRecord mirrors the fields in the legacy scheduled_jobs table.
type LegacyJobRecord struct {
	ID              int64
	ChatID          int64
	PeerType        string
	AccessHash      int64
	ActionType      string
	Payload         string
	IntervalSeconds int
	NextRunAt       time.Time
	CreatedAt       time.Time
	CreatedBy       int64
	Status          string
	MaxAttempts     int
	LastError       string
	ClaimToken      string
	LeaseUntil      sql.NullTime
}

// JobMappingResult represents the mapping disposition for a single legacy row.
type JobMappingResult struct {
	LegacyID      int64           `json:"legacy_id"`
	ActionType    string          `json:"action_type"`
	NewJobID      string          `json:"new_job_id"`
	NewScheduleID string          `json:"new_schedule_id"`
	Status        MigrationStatus `json:"status"`
	Reason        string          `json:"reason,omitempty"`
}

// MigrationReport summarizes the results of a dry-run or actual migration.
type MigrationReport struct {
	TotalLegacyRows  int                `json:"total_legacy_rows"`
	MappedCount      int                `json:"mapped_count"`
	BlockedCount     int                `json:"blocked_count"`
	SkippedCount     int                `json:"skipped_count"`
	Mappings         []JobMappingResult `json:"mappings"`
	ActiveLeaseCount int                `json:"active_lease_count"`
	DueBacklogCount  int                `json:"due_backlog_count"`
	Duration         time.Duration      `json:"duration"`
}

// DeltaReport compares row counts and business checksums between legacy and new schemas.
type DeltaReport struct {
	LegacyRowCount int      `json:"legacy_row_count"`
	NewDefCount    int      `json:"new_def_count"`
	NewSchedCount  int      `json:"new_sched_count"`
	MapCount       int      `json:"map_count"`
	ChecksumMatch  bool     `json:"checksum_match"`
	LegacyChecksum string   `json:"legacy_checksum"`
	NewChecksum    string   `json:"new_checksum"`
	Discrepancies  []string `json:"discrepancies,omitempty"`
}

// Migrator handles the dry-run, execution, delta validation, and reverse projection
// for migrating legacy scheduled_jobs to the redesigned durable jobs schema (ADR 0006 §6).
type Migrator struct {
	db *sql.DB
}

// NewMigrator returns a new Migrator instance.
func NewMigrator(db *sql.DB) *Migrator {
	return &Migrator{db: db}
}

// FetchLegacyRecords loads all rows from scheduled_jobs.
func (m *Migrator) FetchLegacyRecords(ctx context.Context) ([]LegacyJobRecord, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, chat_id, peer_type, access_hash, action_type, payload,
		       interval_seconds, next_run_at, created_at, created_by,
		       status, max_attempts, last_error, claim_token, lease_until
		FROM scheduled_jobs
		ORDER BY id ASC
	`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("query scheduled_jobs: %w", err)
	}
	defer rows.Close()

	var records []LegacyJobRecord
	for rows.Next() {
		var rec LegacyJobRecord
		if err := rows.Scan(
			&rec.ID, &rec.ChatID, &rec.PeerType, &rec.AccessHash, &rec.ActionType, &rec.Payload,
			&rec.IntervalSeconds, &rec.NextRunAt, &rec.CreatedAt, &rec.CreatedBy,
			&rec.Status, &rec.MaxAttempts, &rec.LastError, &rec.ClaimToken, &rec.LeaseUntil,
		); err != nil {
			return nil, fmt.Errorf("scan scheduled_jobs row: %w", err)
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

// DryRun scans all legacy records, evaluates validation rules and existing mappings,
// and returns an exhaustive diff report without altering database state.
func (m *Migrator) DryRun(ctx context.Context) (*MigrationReport, error) {
	start := time.Now()
	records, err := m.FetchLegacyRecords(ctx)
	if err != nil {
		return nil, err
	}

	report := &MigrationReport{
		TotalLegacyRows: len(records),
		Mappings:        make([]JobMappingResult, 0, len(records)),
	}

	now := time.Now().UTC()
	for _, r := range records {
		if r.LeaseUntil.Valid && r.LeaseUntil.Time.After(now) && r.Status == "claimed" {
			report.ActiveLeaseCount++
		}
		if r.NextRunAt.Before(now) && r.Status != "paused" {
			report.DueBacklogCount++
		}

		// Check if already mapped
		var mappedJobID string
		err := m.db.QueryRowContext(ctx, `
			SELECT new_job_id FROM job_migration_map
			WHERE legacy_domain = 'scheduled_jobs' AND legacy_id = ?
		`, strconv.FormatInt(r.ID, 10)).Scan(&mappedJobID)

		if err == nil {
			report.SkippedCount++
			report.Mappings = append(report.Mappings, JobMappingResult{
				LegacyID:   r.ID,
				ActionType: r.ActionType,
				NewJobID:   mappedJobID,
				Status:     StatusSkipped,
				Reason:     "already migrated in job_migration_map",
			})
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("check migration map: %w", err)
		}

		// Validate row integrity
		disposition := m.validateLegacyRecord(r)
		report.Mappings = append(report.Mappings, disposition)
		switch disposition.Status {
		case StatusSuccess:
			report.MappedCount++
		case StatusBlocked:
			report.BlockedCount++
		}
	}

	report.Duration = time.Since(start)
	return report, nil
}

func (m *Migrator) validateLegacyRecord(r LegacyJobRecord) JobMappingResult {
	newJobID := fmt.Sprintf("job:scheduled:%d", r.ID)
	newSchedID := fmt.Sprintf("sched:scheduled:%d", r.ID)

	cleanAction := strings.TrimSpace(r.ActionType)
	if cleanAction == "" {
		return JobMappingResult{
			LegacyID: r.ID, ActionType: r.ActionType, Status: StatusBlocked,
			Reason: "empty action_type",
		}
	}
	if cleanAction != "message" && cleanAction != "command" && cleanAction != "action" {
		return JobMappingResult{
			LegacyID: r.ID, ActionType: r.ActionType, Status: StatusBlocked,
			Reason: fmt.Sprintf("unrecognized legacy action_type: %q", r.ActionType),
		}
	}
	if strings.TrimSpace(r.Payload) == "" {
		return JobMappingResult{
			LegacyID: r.ID, ActionType: r.ActionType, Status: StatusBlocked,
			Reason: "empty payload",
		}
	}

	return JobMappingResult{
		LegacyID:      r.ID,
		ActionType:    r.ActionType,
		NewJobID:      newJobID,
		NewScheduleID: newSchedID,
		Status:        StatusSuccess,
	}
}

// Migrate executes atomic transactional migration of all eligible legacy records
// into job_definitions, job_schedules, and job_migration_map.
func (m *Migrator) Migrate(ctx context.Context) (*MigrationReport, error) {
	start := time.Now()
	records, err := m.FetchLegacyRecords(ctx)
	if err != nil {
		return nil, err
	}

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin migration tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	report := &MigrationReport{
		TotalLegacyRows: len(records),
		Mappings:        make([]JobMappingResult, 0, len(records)),
	}

	for _, r := range records {
		if r.LeaseUntil.Valid && r.LeaseUntil.Time.After(now) && r.Status == "claimed" {
			report.ActiveLeaseCount++
		}
		if r.NextRunAt.Before(now) && r.Status != "paused" {
			report.DueBacklogCount++
		}

		// Check if already mapped
		var existingJobID string
		err := tx.QueryRowContext(ctx, `
			SELECT new_job_id FROM job_migration_map
			WHERE legacy_domain = 'scheduled_jobs' AND legacy_id = ?
		`, strconv.FormatInt(r.ID, 10)).Scan(&existingJobID)

		if err == nil {
			report.SkippedCount++
			report.Mappings = append(report.Mappings, JobMappingResult{
				LegacyID:   r.ID,
				ActionType: r.ActionType,
				NewJobID:   existingJobID,
				Status:     StatusSkipped,
				Reason:     "already migrated",
			})
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("check migration map in tx: %w", err)
		}

		disp := m.validateLegacyRecord(r)
		report.Mappings = append(report.Mappings, disp)
		if disp.Status != StatusSuccess {
			report.BlockedCount++
			continue
		}

		// Build payload wrapper
		payloadMap := map[string]any{
			"legacy_id":    r.ID,
			"chat_id":      r.ChatID,
			"peer_type":    r.PeerType,
			"access_hash":  r.AccessHash,
			"action_type":  r.ActionType,
			"payload_data": r.Payload,
		}
		payloadBytes, err := json.Marshal(payloadMap)
		if err != nil {
			return nil, fmt.Errorf("marshal payload for job %d: %w", r.ID, err)
		}

		enabled := 1
		if r.Status == "paused" {
			enabled = 0
		}

		// 1. Insert into job_definitions
		quotaOwner := fmt.Sprintf("user:%d", r.CreatedBy)
		if r.CreatedBy == 0 {
			quotaOwner = fmt.Sprintf("chat:%d", r.ChatID)
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO job_definitions (
				id, scope_owner, quota_owner, handler_type, version,
				payload, pool, class, timeout_ms, retry_policy, enabled,
				revision, updated_at
			) VALUES (?, ?, ?, ?, 1, ?, 'scheduler', 'normal', 30000, '', ?, 1, ?)
		`, disp.NewJobID, "scheduler", quotaOwner, "scheduler.action", payloadBytes, enabled, now)
		if err != nil {
			return nil, fmt.Errorf("insert job_definition %s: %w", disp.NewJobID, err)
		}

		// 2. Insert into job_schedules
		recurrence := "interval"
		intervalSec := r.IntervalSeconds
		if intervalSec <= 0 {
			recurrence = "once"
			intervalSec = 0
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO job_schedules (
				id, job_id, recurrence, interval_seconds, timezone,
				next_due_at, misfire_policy, overlap_policy, enabled,
				revision, updated_at
			) VALUES (?, ?, ?, ?, 'UTC', ?, 'run_once', 'forbid', ?, 1, ?)
		`, disp.NewScheduleID, disp.NewJobID, recurrence, intervalSec, r.NextRunAt, enabled, now)
		if err != nil {
			return nil, fmt.Errorf("insert job_schedule %s: %w", disp.NewScheduleID, err)
		}

		// 3. Insert into job_migration_map
		_, err = tx.ExecContext(ctx, `
			INSERT INTO job_migration_map (
				legacy_domain, legacy_id, new_job_id, new_schedule_id, migration_revision
			) VALUES ('scheduled_jobs', ?, ?, ?, 1)
		`, strconv.FormatInt(r.ID, 10), disp.NewJobID, disp.NewScheduleID)
		if err != nil {
			return nil, fmt.Errorf("insert job_migration_map for legacy id %d: %w", r.ID, err)
		}

		report.MappedCount++
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit migration tx: %w", err)
	}

	report.Duration = time.Since(start)
	return report, nil
}

// ValidateDelta computes row counts and business checksums across legacy and redesigned tables.
func (m *Migrator) ValidateDelta(ctx context.Context) (*DeltaReport, error) {
	report := &DeltaReport{}

	// 1. Legacy count
	err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduled_jobs`).Scan(&report.LegacyRowCount)
	if err != nil && !strings.Contains(err.Error(), "no such table") {
		return nil, fmt.Errorf("count scheduled_jobs: %w", err)
	}

	// 2. New counts
	_ = m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_definitions WHERE scope_owner = 'scheduler'`).Scan(&report.NewDefCount)
	_ = m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_schedules WHERE id LIKE 'sched:scheduled:%'`).Scan(&report.NewSchedCount)
	_ = m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_migration_map WHERE legacy_domain = 'scheduled_jobs'`).Scan(&report.MapCount)

	// Check for mapping completeness
	if report.LegacyRowCount != report.MapCount {
		report.Discrepancies = append(report.Discrepancies,
			fmt.Sprintf("legacy rows (%d) != migration map entries (%d)", report.LegacyRowCount, report.MapCount))
	}
	if report.NewDefCount != report.NewSchedCount {
		report.Discrepancies = append(report.Discrepancies,
			fmt.Sprintf("new definitions (%d) != new schedules (%d)", report.NewDefCount, report.NewSchedCount))
	}

	// 3. Checksums
	legacyHash, err := m.computeLegacyChecksum(ctx)
	if err != nil {
		return nil, err
	}
	report.LegacyChecksum = legacyHash

	newHash, err := m.computeNewChecksum(ctx)
	if err != nil {
		return nil, err
	}
	report.NewChecksum = newHash
	report.ChecksumMatch = (legacyHash == newHash && len(report.Discrepancies) == 0)

	return report, nil
}

func (m *Migrator) computeLegacyChecksum(ctx context.Context) (string, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id, action_type, payload, interval_seconds FROM scheduled_jobs ORDER BY id ASC`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return "none", nil
		}
		return "", err
	}
	defer rows.Close()

	h := sha256.New()
	for rows.Next() {
		var id int64
		var action, payload string
		var interval int
		if err := rows.Scan(&id, &action, &payload, &interval); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(h, "%d:%s:%s:%d;", id, action, payload, interval)
	}
	return hex.EncodeToString(h.Sum(nil)), rows.Err()
}

func (m *Migrator) computeNewChecksum(ctx context.Context) (string, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT m.legacy_id, d.payload, s.interval_seconds
		FROM job_migration_map m
		JOIN job_definitions d ON d.id = m.new_job_id
		JOIN job_schedules s ON s.id = m.new_schedule_id
		WHERE m.legacy_domain = 'scheduled_jobs'
		ORDER BY CAST(m.legacy_id AS INTEGER) ASC
	`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return "none", nil
		}
		return "", err
	}
	defer rows.Close()

	h := sha256.New()
	for rows.Next() {
		var legacyID string
		var payloadBlob []byte
		var interval int
		if err := rows.Scan(&legacyID, &payloadBlob, &interval); err != nil {
			return "", err
		}
		var pMap map[string]any
		_ = json.Unmarshal(payloadBlob, &pMap)
		action, _ := pMap["action_type"].(string)
		payloadData, _ := pMap["payload_data"].(string)
		_, _ = fmt.Fprintf(h, "%s:%s:%s:%d;", legacyID, action, payloadData, interval)
	}
	return hex.EncodeToString(h.Sum(nil)), rows.Err()
}

// ReverseProject reconciles state back from job_schedules to scheduled_jobs
// for graceful rollback within the rollback window (ADR 0006 §6.3).
func (m *Migrator) ReverseProject(ctx context.Context) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin reverse projection tx: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT m.legacy_id, s.next_due_at, s.enabled
		FROM job_migration_map m
		JOIN job_schedules s ON s.id = m.new_schedule_id
		WHERE m.legacy_domain = 'scheduled_jobs'
	`)
	if err != nil {
		return fmt.Errorf("query new schedules: %w", err)
	}
	defer rows.Close()

	type updateItem struct {
		legacyID int64
		nextRun  time.Time
		status   string
	}
	var updates []updateItem

	for rows.Next() {
		var legIDStr string
		var nextDue time.Time
		var enabled int
		if err := rows.Scan(&legIDStr, &nextDue, &enabled); err != nil {
			return fmt.Errorf("scan schedule update: %w", err)
		}
		id, err := strconv.ParseInt(legIDStr, 10, 64)
		if err != nil {
			continue
		}
		status := "pending"
		if enabled == 0 {
			status = "paused"
		}
		updates = append(updates, updateItem{legacyID: id, nextRun: nextDue, status: status})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, u := range updates {
		_, err := tx.ExecContext(ctx, `
			UPDATE scheduled_jobs
			SET next_run_at = ?, status = ?
			WHERE id = ?
		`, u.nextRun, u.status, u.legacyID)
		if err != nil {
			return fmt.Errorf("reverse project legacy row %d: %w", u.legacyID, err)
		}
	}

	return tx.Commit()
}
