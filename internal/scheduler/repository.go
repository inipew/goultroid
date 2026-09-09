package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrJobLeaseLost is returned when a worker attempts to complete or fail a job whose lease expired
// or was claimed/reclaimed by another worker (fencing token mismatch).
var ErrJobLeaseLost = errors.New("scheduled job lease was lost or claimed by another worker")

// ScheduledJob status constants
const (
	JobStatusPending   = "pending"
	JobStatusRunning   = "running"
	JobStatusFailed    = "failed"
	JobStatusCompleted = "completed"
)

// ScheduledJob represents a scheduled task (one-shot or recurring).
type ScheduledJob struct {
	ID              int64      `json:"id"`
	ChatID          int64      `json:"chat_id"`
	PeerType        string     `json:"peer_type"`   // "user", "chat", "channel", "self"
	AccessHash      int64      `json:"access_hash"` // access hash for user/channel peer resolution
	ActionType      string     `json:"action_type"` // "message" or "command"
	Payload         string     `json:"payload"`     // message text or ".command ..."
	IntervalSeconds int64      `json:"interval_seconds"`
	NextRunAt       time.Time  `json:"next_run_at"`
	CreatedAt       time.Time  `json:"created_at"`
	CreatedBy       int64      `json:"created_by"`
	LastError       string     `json:"last_error"`
	AttemptCount    int        `json:"attempt_count"`
	Status          string     `json:"status"` // "pending", "running", "failed", "completed"
	MaxAttempts     int        `json:"max_attempts"`
	LeaseUntil      *time.Time `json:"lease_until,omitempty"`
	ClaimedAt       *time.Time `json:"claimed_at,omitempty"`
	LastStartedAt   *time.Time `json:"last_started_at,omitempty"`
	LastFinishedAt  *time.Time `json:"last_finished_at,omitempty"`
	ClaimToken      string     `json:"claim_token"`
}

// JobHistoryEntry records a single execution attempt of a scheduled job.
type JobHistoryEntry struct {
	ID         int64     `json:"id"`
	JobID      int64     `json:"job_id"`
	RanAt      time.Time `json:"ran_at"`
	DurationMs int64     `json:"duration_ms"`
	Success    bool      `json:"success"`
	ErrorMsg   string    `json:"error_msg,omitempty"`
}

// Repository is the persistence contract for the scheduler domain.
type Repository interface {
	CreateScheduledJob(ctx context.Context, job *ScheduledJob) (*ScheduledJob, error)
	GetScheduledJob(ctx context.Context, id int64) (*ScheduledJob, error)
	ListScheduledJobs(ctx context.Context, chatID int64) ([]ScheduledJob, error)
	ListDueScheduledJobs(ctx context.Context, before time.Time) ([]ScheduledJob, error)
	ClaimDueScheduledJobs(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]ScheduledJob, error)
	CompleteScheduledJob(ctx context.Context, id int64, claimToken string, durationMs int64, now time.Time) error
	FailScheduledJob(ctx context.Context, id int64, claimToken string, lastError string, durationMs int64, retryDelay time.Duration, isPermanent bool, now time.Time) error
	UpdateScheduledJobNextRun(ctx context.Context, id int64, nextRun time.Time) error
	RecordJobFailure(ctx context.Context, id int64, lastError string) error
	DeleteScheduledJob(ctx context.Context, id int64) error
	RenewJobLease(ctx context.Context, id int64, claimToken string, extension time.Duration, now time.Time) error
	RecordJobRun(ctx context.Context, entry *JobHistoryEntry) error
	GetJobHistory(ctx context.Context, jobID int64, limit int) ([]JobHistoryEntry, error)
}

// SQLiteRepository is a SQLite-backed implementation of Repository.
type SQLiteRepository struct {
	db *sql.DB
}

// NewSQLiteRepository creates a new SQLite-backed scheduler repository.
func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

var _ Repository = (*SQLiteRepository)(nil)

const scheduledJobColumns = `id, chat_id, peer_type, access_hash, action_type, payload, interval_seconds, next_run_at, created_at, created_by, last_error, attempt_count, status, max_attempts, lease_until, claimed_at, last_started_at, last_finished_at, claim_token`

func scanScheduledJob(scanner interface{ Scan(dest ...any) error }) (*ScheduledJob, error) {
	var job ScheduledJob
	var leaseUntil, claimedAt, lastStartedAt, lastFinishedAt sql.NullTime
	if err := scanner.Scan(
		&job.ID, &job.ChatID, &job.PeerType, &job.AccessHash, &job.ActionType,
		&job.Payload, &job.IntervalSeconds, &job.NextRunAt, &job.CreatedAt,
		&job.CreatedBy, &job.LastError, &job.AttemptCount,
		&job.Status, &job.MaxAttempts,
		&leaseUntil, &claimedAt, &lastStartedAt, &lastFinishedAt,
		&job.ClaimToken,
	); err != nil {
		return nil, err
	}
	if leaseUntil.Valid {
		t := leaseUntil.Time
		job.LeaseUntil = &t
	}
	if claimedAt.Valid {
		t := claimedAt.Time
		job.ClaimedAt = &t
	}
	if lastStartedAt.Valid {
		t := lastStartedAt.Time
		job.LastStartedAt = &t
	}
	if lastFinishedAt.Valid {
		t := lastFinishedAt.Time
		job.LastFinishedAt = &t
	}
	return &job, nil
}

// InitSchema creates the scheduled_jobs and scheduled_job_history tables if they do not exist.
func (r *SQLiteRepository) InitSchema(ctx context.Context) error {
	schema := `
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
	);
	CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_next_run ON scheduled_jobs(next_run_at);
	CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_chat_id ON scheduled_jobs(chat_id);
	CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_claim ON scheduled_jobs(status, next_run_at, lease_until);

	CREATE TABLE IF NOT EXISTS scheduled_job_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		job_id INTEGER NOT NULL,
		ran_at DATETIME NOT NULL,
		duration_ms INTEGER NOT NULL,
		success BOOLEAN NOT NULL,
		error_msg TEXT DEFAULT '',
		FOREIGN KEY (job_id) REFERENCES scheduled_jobs(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_scheduled_job_history_job ON scheduled_job_history(job_id, ran_at);
	`
	_, err := r.db.ExecContext(ctx, schema)
	return err
}

// CreateScheduledJob inserts a new scheduled job into the database.
func (r *SQLiteRepository) CreateScheduledJob(ctx context.Context, job *ScheduledJob) (*ScheduledJob, error) {
	if job == nil {
		return nil, errors.New("job cannot be nil")
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	if job.Status == "" {
		job.Status = JobStatusPending
	}
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = 3
	}

	query := `INSERT INTO scheduled_jobs (
		chat_id, peer_type, access_hash, action_type, payload,
		interval_seconds, next_run_at, created_at, created_by,
		last_error, attempt_count, status, max_attempts,
		lease_until, claimed_at, last_started_at, last_finished_at,
		claim_token
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	res, err := r.db.ExecContext(ctx, query,
		job.ChatID, job.PeerType, job.AccessHash, job.ActionType, job.Payload,
		job.IntervalSeconds, job.NextRunAt, job.CreatedAt, job.CreatedBy,
		job.LastError, job.AttemptCount, job.Status, job.MaxAttempts,
		job.LeaseUntil, job.ClaimedAt, job.LastStartedAt, job.LastFinishedAt,
		job.ClaimToken,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert scheduled job: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve last insert id: %w", err)
	}
	job.ID = id
	return job, nil
}

// GetScheduledJob retrieves a single scheduled job by its primary key ID.
func (r *SQLiteRepository) GetScheduledJob(ctx context.Context, id int64) (*ScheduledJob, error) {
	query := `SELECT ` + scheduledJobColumns + ` FROM scheduled_jobs WHERE id = ?`
	row := r.db.QueryRowContext(ctx, query, id)
	job, err := scanScheduledJob(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get scheduled job: %w", err)
	}
	return job, nil
}

// ListScheduledJobs lists all scheduled jobs for a particular chat ID.
func (r *SQLiteRepository) ListScheduledJobs(ctx context.Context, chatID int64) ([]ScheduledJob, error) {
	query := `SELECT ` + scheduledJobColumns + ` FROM scheduled_jobs WHERE chat_id = ? ORDER BY next_run_at ASC`
	rows, err := r.db.QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list scheduled jobs: %w", err)
	}
	defer rows.Close()

	var jobs []ScheduledJob
	for rows.Next() {
		job, err := scanScheduledJob(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan scheduled job: %w", err)
		}
		jobs = append(jobs, *job)
	}
	return jobs, rows.Err()
}

// ListDueScheduledJobs lists scheduled jobs whose next run time is on or before `before`.
func (r *SQLiteRepository) ListDueScheduledJobs(ctx context.Context, before time.Time) ([]ScheduledJob, error) {
	query := `SELECT ` + scheduledJobColumns + `
		FROM scheduled_jobs
		WHERE (status = 'pending' AND next_run_at <= ?)
		   OR (status = 'running' AND lease_until IS NOT NULL AND lease_until < ? AND next_run_at <= ?)
		ORDER BY next_run_at ASC`
	rows, err := r.db.QueryContext(ctx, query, before, before, before)
	if err != nil {
		return nil, fmt.Errorf("failed to list due scheduled jobs: %w", err)
	}
	defer rows.Close()

	var jobs []ScheduledJob
	for rows.Next() {
		job, err := scanScheduledJob(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan due scheduled job: %w", err)
		}
		jobs = append(jobs, *job)
	}
	return jobs, rows.Err()
}

func generateClaimToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ClaimDueScheduledJobs attempts to claim due jobs with exponential backoff retry on SQLite locking.
func (r *SQLiteRepository) ClaimDueScheduledJobs(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]ScheduledJob, error) {
	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		jobs, err := r.claimDueScheduledJobsOnce(ctx, now, limit, lease)
		if err == nil {
			return jobs, nil
		}
		if !isRetryableSQLiteLock(err) || attempt == maxAttempts-1 {
			return nil, err
		}
		delay := time.Duration(25*(1<<attempt)) * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, errors.New("scheduled job claim retry loop exhausted")
}

func isRetryableSQLiteLock(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "SQLITE_BUSY") ||
		strings.Contains(message, "SQLITE_LOCKED") ||
		strings.Contains(message, "database is locked")
}

func (r *SQLiteRepository) claimDueScheduledJobsOnce(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]ScheduledJob, error) {
	if limit <= 0 {
		limit = 10
	}
	if lease <= 0 {
		lease = 90 * time.Second
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to begin claim transaction: %w", err)
	}
	defer tx.Rollback()

	query := `SELECT ` + scheduledJobColumns + `
	          FROM scheduled_jobs
	          WHERE (status = 'pending' OR (status = 'running' AND lease_until IS NOT NULL AND lease_until < ?))
	            AND next_run_at <= ?
	          ORDER BY next_run_at ASC
	          LIMIT ?`

	rows, err := tx.QueryContext(ctx, query, now, now, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query due scheduled jobs for claim: %w", err)
	}

	var jobs []ScheduledJob
	for rows.Next() {
		job, err := scanScheduledJob(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan claimable scheduled job: %w", err)
		}
		jobs = append(jobs, *job)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	if len(jobs) == 0 {
		return nil, nil
	}

	leaseUntil := now.Add(lease)
	updateStmt, err := tx.PrepareContext(ctx, `
		UPDATE scheduled_jobs
		SET status = 'running',
		    lease_until = ?,
		    claimed_at = ?,
		    last_started_at = ?,
		    attempt_count = attempt_count + 1,
		    claim_token = ?,
		    last_error = ?
		WHERE id = ?
		  AND (status = 'pending' OR (status = 'running' AND lease_until IS NOT NULL AND lease_until < ?))
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare claim update statement: %w", err)
	}
	defer updateStmt.Close()

	histStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO scheduled_job_history (job_id, ran_at, duration_ms, success, error_msg)
		VALUES (?, ?, ?, 0, ?)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare claim history statement: %w", err)
	}
	defer histStmt.Close()

	var claimedJobs []ScheduledJob
	for i := range jobs {
		lastErr := jobs[i].LastError
		if jobs[i].Status == JobStatusRunning {
			lastErr = "previous execution lease expired"
			if _, err := histStmt.ExecContext(ctx, jobs[i].ID, now, 0, "execution lease expired (previous worker abandoned or crashed)"); err != nil {
				return nil, fmt.Errorf("failed to record lease expiration history for job %d: %w", jobs[i].ID, err)
			}
		}

		token := generateClaimToken()
		res, err := updateStmt.ExecContext(ctx, leaseUntil, now, now, token, lastErr, jobs[i].ID, now)
		if err != nil {
			return nil, fmt.Errorf("failed to update claimed job %d: %w", jobs[i].ID, err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rows == 1 {
			jobs[i].Status = JobStatusRunning
			jobs[i].LeaseUntil = &leaseUntil
			jobs[i].ClaimedAt = &now
			jobs[i].LastStartedAt = &now
			jobs[i].AttemptCount++
			jobs[i].ClaimToken = token
			jobs[i].LastError = lastErr
			claimedJobs = append(claimedJobs, jobs[i])
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit claim transaction: %w", err)
	}

	return claimedJobs, nil
}

// CompleteScheduledJob marks a job complete or advances its recurring schedule.
func (r *SQLiteRepository) CompleteScheduledJob(ctx context.Context, id int64, claimToken string, durationMs int64, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var intervalSeconds int64
	var nextRunAt time.Time
	err = tx.QueryRowContext(ctx, "SELECT interval_seconds, next_run_at FROM scheduled_jobs WHERE id = ? AND status = 'running' AND claim_token = ?", id, claimToken).Scan(&intervalSeconds, &nextRunAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrJobLeaseLost
		}
		return fmt.Errorf("failed to fetch job state for completion: %w", err)
	}

	if intervalSeconds > 0 {
		// Anchored next run calculation with SkipMissed policy to eliminate clock drift
		nextRun := nextRunAt.Add(time.Duration(intervalSeconds) * time.Second)
		for !nextRun.After(now) {
			nextRun = nextRun.Add(time.Duration(intervalSeconds) * time.Second)
		}

		res, err := tx.ExecContext(ctx, `
			UPDATE scheduled_jobs
			SET status = 'pending',
			    next_run_at = ?,
			    attempt_count = 0,
			    last_error = '',
			    lease_until = NULL,
			    claimed_at = NULL,
			    claim_token = '',
			    last_finished_at = ?
			WHERE id = ? AND status = 'running' AND claim_token = ?
		`, nextRun, now, id, claimToken)
		if err != nil {
			return fmt.Errorf("failed to update recurring job: %w", err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return ErrJobLeaseLost
		}
	} else {
		res, err := tx.ExecContext(ctx, "DELETE FROM scheduled_jobs WHERE id = ? AND status = 'running' AND claim_token = ?", id, claimToken)
		if err != nil {
			return fmt.Errorf("failed to delete completed job: %w", err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return ErrJobLeaseLost
		}
	}

	// Atomically record execution history in the same transaction
	histQuery := `
		INSERT INTO scheduled_job_history (job_id, ran_at, duration_ms, success, error_msg)
		VALUES (?, ?, ?, 1, '')`
	if _, err := tx.ExecContext(ctx, histQuery, id, now, durationMs); err != nil {
		return fmt.Errorf("failed to record completion history: %w", err)
	}

	return tx.Commit()
}

// FailScheduledJob marks a job failed or schedules a retry.
func (r *SQLiteRepository) FailScheduledJob(ctx context.Context, id int64, claimToken string, lastError string, durationMs int64, retryDelay time.Duration, isPermanent bool, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var attemptCount, maxAttempts int
	err = tx.QueryRowContext(ctx, "SELECT attempt_count, max_attempts FROM scheduled_jobs WHERE id = ? AND status = 'running' AND claim_token = ?", id, claimToken).Scan(&attemptCount, &maxAttempts)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrJobLeaseLost
		}
		return fmt.Errorf("failed to fetch job state for failure: %w", err)
	}

	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	var res sql.Result
	if isPermanent || attemptCount >= maxAttempts {
		res, err = tx.ExecContext(ctx, `
			UPDATE scheduled_jobs
			SET status = 'failed',
			    last_error = ?,
			    lease_until = NULL,
			    claim_token = '',
			    last_finished_at = ?
			WHERE id = ? AND status = 'running' AND claim_token = ?
		`, lastError, now, id, claimToken)
	} else {
		nextRun := now.Add(retryDelay)
		res, err = tx.ExecContext(ctx, `
			UPDATE scheduled_jobs
			SET status = 'pending',
			    next_run_at = ?,
			    last_error = ?,
			    lease_until = NULL,
			    claim_token = '',
			    last_finished_at = ?
			WHERE id = ? AND status = 'running' AND claim_token = ?
		`, nextRun, lastError, now, id, claimToken)
	}
	if err != nil {
		return fmt.Errorf("failed to update failed scheduled job: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrJobLeaseLost
	}

	// Atomically record execution failure history in the same transaction
	histQuery := `
		INSERT INTO scheduled_job_history (job_id, ran_at, duration_ms, success, error_msg)
		VALUES (?, ?, ?, 0, ?)`
	if _, err := tx.ExecContext(ctx, histQuery, id, now, durationMs, lastError); err != nil {
		return fmt.Errorf("failed to record failure history: %w", err)
	}

	return tx.Commit()
}

// UpdateScheduledJobNextRun updates the next_run_at timestamp for a scheduled job.
func (r *SQLiteRepository) UpdateScheduledJobNextRun(ctx context.Context, id int64, nextRun time.Time) error {
	query := "UPDATE scheduled_jobs SET next_run_at = ? WHERE id = ?"
	res, err := r.db.ExecContext(ctx, query, nextRun, id)
	if err != nil {
		return fmt.Errorf("failed to update scheduled job next_run_at: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("scheduled job not found")
	}
	return nil
}

// RecordJobFailure increments the attempt count and records the error message.
func (r *SQLiteRepository) RecordJobFailure(ctx context.Context, id int64, lastError string) error {
	query := "UPDATE scheduled_jobs SET attempt_count = attempt_count + 1, last_error = ? WHERE id = ?"
	res, err := r.db.ExecContext(ctx, query, lastError, id)
	if err != nil {
		return fmt.Errorf("failed to record scheduled job failure: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("scheduled job not found")
	}
	return nil
}

// DeleteScheduledJob deletes a scheduled job by its primary key ID.
func (r *SQLiteRepository) DeleteScheduledJob(ctx context.Context, id int64) error {
	query := "DELETE FROM scheduled_jobs WHERE id = ?"
	res, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete scheduled job: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("scheduled job not found")
	}
	return nil
}

// RecordJobRun inserts a history entry for a single scheduler job execution attempt.
func (r *SQLiteRepository) RecordJobRun(ctx context.Context, entry *JobHistoryEntry) error {
	if entry == nil {
		return fmt.Errorf("job history entry cannot be nil")
	}
	query := `
		INSERT INTO scheduled_job_history (job_id, ran_at, duration_ms, success, error_msg)
		VALUES (?, ?, ?, ?, ?)`
	if _, err := r.db.ExecContext(ctx, query, entry.JobID, entry.RanAt, entry.DurationMs, entry.Success, entry.ErrorMsg); err != nil {
		return fmt.Errorf("failed to record job run: %w", err)
	}
	return nil
}

// GetJobHistory returns the most recent execution history entries for a scheduled job,
// ordered newest-first. Limit <= 0 defaults to 20.
func (r *SQLiteRepository) GetJobHistory(ctx context.Context, jobID int64, limit int) ([]JobHistoryEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT id, job_id, ran_at, duration_ms, success, error_msg
		FROM scheduled_job_history
		WHERE job_id = ?
		ORDER BY ran_at DESC
		LIMIT ?`
	rows, err := r.db.QueryContext(ctx, query, jobID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query job history: %w", err)
	}
	defer rows.Close()

	var entries []JobHistoryEntry
	for rows.Next() {
		var e JobHistoryEntry
		if err := rows.Scan(&e.ID, &e.JobID, &e.RanAt, &e.DurationMs, &e.Success, &e.ErrorMsg); err != nil {
			return nil, fmt.Errorf("failed to scan job history entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// RenewJobLease extends the lease duration of a currently running scheduled job.
// Returns ErrJobLeaseLost if the job is no longer running with the specified claimToken.
func (r *SQLiteRepository) RenewJobLease(ctx context.Context, id int64, claimToken string, extension time.Duration, now time.Time) error {
	newLease := now.Add(extension)
	query := `
		UPDATE scheduled_jobs
		SET lease_until = ?
		WHERE id = ? AND status = 'running' AND claim_token = ?`
	res, err := r.db.ExecContext(ctx, query, newLease, id, claimToken)
	if err != nil {
		return fmt.Errorf("failed to renew job lease: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrJobLeaseLost
	}
	return nil
}
