package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Repository provides durable persistence for managed jobs.
type Repository interface {
	InitSchema(ctx context.Context) error
	Save(ctx context.Context, job *Job) error
	UpdateState(ctx context.Context, id string, state JobState, lastError string, lastRun, nextRun time.Time) error
	Get(ctx context.Context, id string) (*Job, error)
	ListActive(ctx context.Context) ([]*Job, error)
	ListByOwner(ctx context.Context, owner string) ([]*Job, error)
	Delete(ctx context.Context, id string) error
	DeleteByOwner(ctx context.Context, owner string) (int, error)
	DeleteTerminalBefore(ctx context.Context, before time.Time) (int, error)
}

// SQLiteRepository is a SQLite-backed implementation of Repository.
type SQLiteRepository struct {
	db *sql.DB
}

// NewSQLiteRepository creates a new SQLiteRepository.
func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

// InitSchema initializes the managed_jobs table and indexes if they do not exist.
func (r *SQLiteRepository) InitSchema(ctx context.Context) error {
	query := `
	CREATE TABLE IF NOT EXISTS managed_jobs (
		id TEXT PRIMARY KEY,
		owner TEXT NOT NULL,
		type TEXT NOT NULL,
		schedule TEXT NOT NULL,
		payload BLOB,
		recovery_policy TEXT NOT NULL,
		idempotency_key TEXT,
		timeout_ms INTEGER NOT NULL DEFAULT 0,
		pool TEXT NOT NULL DEFAULT 'general',
		next_run_at DATETIME,
		last_run_at DATETIME,
		state TEXT NOT NULL,
		last_error TEXT,
		updated_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_managed_jobs_owner ON managed_jobs(owner);
	CREATE INDEX IF NOT EXISTS idx_managed_jobs_state ON managed_jobs(state);
	`
	_, err := r.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to init managed_jobs schema: %w", err)
	}
	return nil
}

// Save inserts or updates a managed job.
func (r *SQLiteRepository) Save(ctx context.Context, j *Job) error {
	query := `
	INSERT INTO managed_jobs (
		id, owner, type, schedule, payload, recovery_policy, idempotency_key,
		timeout_ms, pool, next_run_at, last_run_at, state, last_error, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		owner = excluded.owner,
		type = excluded.type,
		schedule = excluded.schedule,
		payload = excluded.payload,
		recovery_policy = excluded.recovery_policy,
		idempotency_key = excluded.idempotency_key,
		timeout_ms = excluded.timeout_ms,
		pool = excluded.pool,
		next_run_at = excluded.next_run_at,
		last_run_at = excluded.last_run_at,
		state = excluded.state,
		last_error = excluded.last_error,
		updated_at = excluded.updated_at;
	`
	pool := j.Pool
	if pool == "" {
		pool = "general"
	}
	timeoutMs := j.Timeout.Milliseconds()
	now := time.Now().UTC()

	var nextRun, lastRun *time.Time
	if !j.NextRun.IsZero() {
		nextRun = &j.NextRun
	}
	if !j.LastRun.IsZero() {
		lastRun = &j.LastRun
	}

	_, err := r.db.ExecContext(ctx, query,
		j.ID,
		j.Owner,
		j.Type,
		j.Schedule,
		j.Payload,
		string(j.RecoveryPolicy),
		j.IdempotencyKey,
		timeoutMs,
		pool,
		nextRun,
		lastRun,
		string(j.State),
		j.LastError,
		now,
	)
	if err != nil {
		return fmt.Errorf("failed to save managed job %q: %w", j.ID, err)
	}
	return nil
}

// UpdateState updates the lifecycle state, execution timestamp, and error for a job.
func (r *SQLiteRepository) UpdateState(ctx context.Context, id string, state JobState, lastError string, lastRun, nextRun time.Time) error {
	query := `
	UPDATE managed_jobs
	SET state = ?,
	    last_error = ?,
	    last_run_at = ?,
	    next_run_at = ?,
	    updated_at = ?
	WHERE id = ?;
	`
	now := time.Now().UTC()
	var nextRunPtr, lastRunPtr *time.Time
	if !nextRun.IsZero() {
		nextRunPtr = &nextRun
	}
	if !lastRun.IsZero() {
		lastRunPtr = &lastRun
	}

	res, err := r.db.ExecContext(ctx, query, string(state), lastError, lastRunPtr, nextRunPtr, now, id)
	if err != nil {
		return fmt.Errorf("failed to update managed job %q state: %w", id, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("managed job %q not found", id)
	}
	return nil
}

// Get retrieves a job by ID.
func (r *SQLiteRepository) Get(ctx context.Context, id string) (*Job, error) {
	query := `
	SELECT id, owner, type, schedule, payload, recovery_policy, idempotency_key,
	       timeout_ms, pool, next_run_at, last_run_at, state, last_error
	FROM managed_jobs
	WHERE id = ?;
	`
	row := r.db.QueryRowContext(ctx, query, id)
	job, err := scanJobRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("managed job %q not found", id)
		}
		return nil, err
	}
	return job, nil
}

// ListActive retrieves all jobs that are not completed, cancelled, or failed.
func (r *SQLiteRepository) ListActive(ctx context.Context) ([]*Job, error) {
	query := `
	SELECT id, owner, type, schedule, payload, recovery_policy, idempotency_key,
	       timeout_ms, pool, next_run_at, last_run_at, state, last_error
	FROM managed_jobs
	WHERE state IN ('registered', 'scheduled', 'triggered', 'executing')
	ORDER BY next_run_at ASC;
	`
	return r.queryJobs(ctx, query)
}

// ListByOwner retrieves all jobs belonging to an owner.
func (r *SQLiteRepository) ListByOwner(ctx context.Context, owner string) ([]*Job, error) {
	query := `
	SELECT id, owner, type, schedule, payload, recovery_policy, idempotency_key,
	       timeout_ms, pool, next_run_at, last_run_at, state, last_error
	FROM managed_jobs
	WHERE owner = ?
	ORDER BY id ASC;
	`
	return r.queryJobs(ctx, query, owner)
}

// Delete removes a job by ID.
func (r *SQLiteRepository) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM managed_jobs WHERE id = ?;`
	_, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete managed job %q: %w", id, err)
	}
	return nil
}

// DeleteByOwner removes all jobs belonging to an owner.
func (r *SQLiteRepository) DeleteByOwner(ctx context.Context, owner string) (int, error) {
	query := `DELETE FROM managed_jobs WHERE owner = ?;`
	res, err := r.db.ExecContext(ctx, query, owner)
	if err != nil {
		return 0, fmt.Errorf("failed to delete managed jobs for owner %q: %w", owner, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rows), nil
}

// DeleteTerminalBefore removes completed, failed, and cancelled jobs older than before.
func (r *SQLiteRepository) DeleteTerminalBefore(ctx context.Context, before time.Time) (int, error) {
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM managed_jobs
		WHERE state IN ('completed', 'failed', 'cancelled') AND updated_at < ?;
	`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("delete expired terminal jobs: %w", err)
	}
	rows, err := result.RowsAffected()
	return int(rows), err
}

func (r *SQLiteRepository) queryJobs(ctx context.Context, query string, args ...any) ([]*Job, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query managed jobs: %w", err)
	}
	defer rows.Close()

	var result []*Job
	for rows.Next() {
		job, err := scanJobRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanJobRow(s scannable) (*Job, error) {
	var (
		j                Job
		recPol           string
		stateStr         string
		timeoutMs        int64
		nextRun, lastRun sql.NullTime
		idempKey         sql.NullString
		lastErr          sql.NullString
	)

	err := s.Scan(
		&j.ID,
		&j.Owner,
		&j.Type,
		&j.Schedule,
		&j.Payload,
		&recPol,
		&idempKey,
		&timeoutMs,
		&j.Pool,
		&nextRun,
		&lastRun,
		&stateStr,
		&lastErr,
	)
	if err != nil {
		return nil, err
	}

	j.RecoveryPolicy = RecoveryPolicy(recPol)
	j.State = JobState(stateStr)
	if idempKey.Valid {
		j.IdempotencyKey = idempKey.String
	}
	if lastErr.Valid {
		j.LastError = lastErr.String
	}
	if timeoutMs > 0 {
		j.Timeout = time.Duration(timeoutMs) * time.Millisecond
	}
	if nextRun.Valid {
		j.NextRun = nextRun.Time
	}
	if lastRun.Valid {
		j.LastRun = lastRun.Time
	}

	return &j, nil
}
