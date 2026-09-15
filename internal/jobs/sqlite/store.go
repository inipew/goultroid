package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

var (
	ErrDefinitionNotFound = errors.New("job definition not found")
	ErrScheduleNotFound   = errors.New("job schedule not found")
	ErrOccurrenceNotFound = errors.New("job occurrence not found")
	ErrAttemptNotFound    = errors.New("job attempt not found")
	ErrLeaseFencingLost   = errors.New("lease fencing lost or stale epoch")
	ErrOccurrenceNotReady = errors.New("occurrence is not in ready state")
)

// Store provides persistence transactions for redesigned job definitions, schedules, occurrences, and attempts.
type Store struct {
	db *sql.DB
}

// NewStore creates a new Store instance.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// SaveDefinition saves or updates a JobDefinition.
func (s *Store) SaveDefinition(ctx context.Context, def *jobs.JobDefinition) error {
	query := `
	INSERT INTO job_definitions (
		id, scope_owner, quota_owner, handler_type, version, payload,
		pool, class, timeout_ms, retry_policy, enabled, revision, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		scope_owner = excluded.scope_owner,
		quota_owner = excluded.quota_owner,
		handler_type = excluded.handler_type,
		version = excluded.version,
		payload = excluded.payload,
		pool = excluded.pool,
		class = excluded.class,
		timeout_ms = excluded.timeout_ms,
		retry_policy = excluded.retry_policy,
		enabled = excluded.enabled,
		revision = revision + 1,
		updated_at = excluded.updated_at;
	`
	retryPolicyBytes, err := json.Marshal(def.RetryPolicy)
	if err != nil {
		return fmt.Errorf("encode retry policy: %w", err)
	}
	now := time.Now().UTC()
	timeoutMs := def.Timeout.Milliseconds()
	enabledInt := 0
	if def.Enabled {
		enabledInt = 1
	}

	_, err = s.db.ExecContext(ctx, query,
		def.ID, def.ScopeOwner, def.QuotaOwner, def.HandlerType, def.Version, def.Payload,
		def.Pool, def.Class, timeoutMs, string(retryPolicyBytes), enabledInt, def.Revision, now,
	)
	if err != nil {
		return fmt.Errorf("failed to save job definition %s: %w", def.ID, err)
	}
	return nil
}

// GetDefinition loads a JobDefinition by ID.
func (s *Store) GetDefinition(ctx context.Context, id string) (*jobs.JobDefinition, error) {
	query := `
	SELECT id, scope_owner, quota_owner, handler_type, version, payload,
	       pool, class, timeout_ms, retry_policy, enabled, revision
	FROM job_definitions WHERE id = ?;
	`
	row := s.db.QueryRowContext(ctx, query, id)
	var def jobs.JobDefinition
	var timeoutMs int64
	var retryPolicyStr string
	var enabledInt int

	err := row.Scan(
		&def.ID, &def.ScopeOwner, &def.QuotaOwner, &def.HandlerType, &def.Version, &def.Payload,
		&def.Pool, &def.Class, &timeoutMs, &retryPolicyStr, &enabledInt, &def.Revision,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDefinitionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query job definition %s: %w", id, err)
	}

	def.Timeout = time.Duration(timeoutMs) * time.Millisecond
	def.Enabled = enabledInt == 1
	if err := json.Unmarshal([]byte(retryPolicyStr), &def.RetryPolicy); err != nil {
		return nil, fmt.Errorf("decode retry policy: %w", err)
	}
	return &def, nil
}

// MaterializeOccurrence inserts a unique occurrence for a scheduled or manual trigger (ADR 0006 §7.3).
func (s *Store) MaterializeOccurrence(ctx context.Context, occ *jobs.JobOccurrence) error {
	query := `
	INSERT INTO job_occurrences (
		id, job_id, schedule_id, scheduled_for, occurrence_key, state, ready_at, cancel_epoch, revision, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	now := time.Now().UTC()
	if occ.ReadyAt.IsZero() {
		occ.ReadyAt = now
	}
	if occ.State == "" {
		occ.State = jobs.OccurrenceReady
	}

	var schedID sql.NullString
	if occ.ScheduleID != "" {
		schedID = sql.NullString{String: occ.ScheduleID, Valid: true}
	}

	_, err := s.db.ExecContext(ctx, query,
		occ.ID, occ.JobID, schedID, occ.ScheduledFor, occ.OccurrenceKey,
		string(occ.State), occ.ReadyAt, occ.CancelEpoch, occ.Revision, now,
	)
	if err != nil {
		return fmt.Errorf("failed to materialize occurrence %s: %w", occ.ID, err)
	}
	return nil
}

// PrepareAttemptLease atomically creates an attempt and leases the occurrence under writer intent (ADR 0006 §7.3 & §7.4).
func (s *Store) PrepareAttemptLease(ctx context.Context, occurrenceID, taskID string, leaseDuration time.Duration) (*jobs.JobAttempt, error) {
	if leaseDuration <= 0 || taskID == "" {
		return nil, errors.New("invalid attempt lease request")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin prepare attempt lease: %w", err)
	}
	defer tx.Rollback()

	// Acquire writer intent before any reads to avoid SQLite read-to-write upgrades.
	if _, err := tx.ExecContext(ctx, `UPDATE job_occurrences SET revision = revision WHERE id = ?`, occurrenceID); err != nil {
		return nil, fmt.Errorf("acquire prepare writer intent: %w", err)
	}

	// Check occurrence state and cancel epoch
	var occState string
	var cancelEpoch uint64
	var occRev uint64
	var readyAt time.Time
	checkQuery := `SELECT state, cancel_epoch, revision, ready_at FROM job_occurrences WHERE id = ?;`
	if err := tx.QueryRowContext(ctx, checkQuery, occurrenceID).Scan(&occState, &cancelEpoch, &occRev, &readyAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOccurrenceNotFound
		}
		return nil, err
	}

	if occState != string(jobs.OccurrenceReady) || readyAt.After(time.Now().UTC()) {
		return nil, fmt.Errorf("%w: occurrence state is %s", ErrOccurrenceNotReady, occState)
	}

	// Count existing attempts to determine attempt_no
	var attemptCount int
	countQuery := `SELECT COUNT(*) FROM job_attempts WHERE occurrence_id = ?;`
	if err := tx.QueryRowContext(ctx, countQuery, occurrenceID).Scan(&attemptCount); err != nil {
		return nil, err
	}
	attemptNo := attemptCount + 1

	now := time.Now().UTC()
	leaseEpoch := uint64(attemptNo)
	leaseUntil := now.Add(leaseDuration)
	attemptID := fmt.Sprintf("attempt:%s:%d", occurrenceID, attemptNo)

	insertAttempt := `
	INSERT INTO job_attempts (
		id, occurrence_id, attempt_no, task_id, lease_epoch, lease_until, state, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?);
	`
	if _, err := tx.ExecContext(ctx, insertAttempt,
		attemptID, occurrenceID, attemptNo, taskID, leaseEpoch, leaseUntil, string(jobs.AttemptLeased), now,
	); err != nil {
		return nil, fmt.Errorf("insert job attempt: %w", err)
	}

	updateOcc := `
	UPDATE job_occurrences SET state = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND state = 'ready' AND revision = ? AND cancel_epoch = ?;
	`
	if _, err := tx.ExecContext(ctx, updateOcc, string(jobs.OccurrenceDispatched), now, occurrenceID, occRev, cancelEpoch); err != nil {
		return nil, fmt.Errorf("update occurrence state: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit prepare attempt lease: %w", err)
	}

	return &jobs.JobAttempt{
		ID:           attemptID,
		OccurrenceID: occurrenceID,
		AttemptNo:    attemptNo,
		TaskID:       taskID,
		LeaseEpoch:   leaseEpoch,
		LeaseUntil:   leaseUntil,
		State:        jobs.AttemptLeased,
	}, nil
}

// CommitAttemptResult applies final attempt outcome using lease epoch fencing (ADR 0006 §7.5).
func (s *Store) CommitAttemptResult(ctx context.Context, attemptID string, leaseEpoch uint64, outcome jobs.AttemptState, result []byte, errStr string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin commit attempt result: %w", err)
	}
	defer tx.Rollback()

	switch outcome {
	case jobs.AttemptCompleted, jobs.AttemptFailed, jobs.AttemptTimedOut, jobs.AttemptCancelled, jobs.AttemptAbortedBeforeStart:
	default:
		return errors.New("attempt result must be terminal")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE job_attempts SET lease_epoch = lease_epoch WHERE id = ?`, attemptID); err != nil {
		return fmt.Errorf("acquire completion writer intent: %w", err)
	}
	var occID, state, oldError string
	var epoch uint64
	var oldResult []byte
	var attemptNo int
	if err := tx.QueryRowContext(ctx, `SELECT occurrence_id, lease_epoch, state, result, COALESCE(error, ''), attempt_no FROM job_attempts WHERE id = ?`, attemptID).Scan(&occID, &epoch, &state, &oldResult, &oldError, &attemptNo); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLeaseFencingLost
		}
		return err
	}
	if epoch != leaseEpoch {
		return ErrLeaseFencingLost
	}
	if state != string(jobs.AttemptLeased) && state != string(jobs.AttemptRunning) {
		if state == string(outcome) && bytes.Equal(oldResult, result) && oldError == errStr {
			return tx.Commit() // Lost acknowledgement replay; preserve the original result and timestamps.
		}
		return ErrLeaseFencingLost
	}
	var latest int
	var occState string
	if err := tx.QueryRowContext(ctx, `SELECT state, (SELECT MAX(attempt_no) FROM job_attempts WHERE occurrence_id = ?) FROM job_occurrences WHERE id = ?`, occID, occID).Scan(&occState, &latest); err != nil {
		return err
	}
	if latest != attemptNo || occState != string(jobs.OccurrenceDispatched) {
		return ErrLeaseFencingLost
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE job_attempts SET state = ?, result = ?, error = ?, finished_at = ? WHERE id = ? AND lease_epoch = ?`, string(outcome), result, errStr, now, attemptID, leaseEpoch); err != nil {
		return fmt.Errorf("commit attempt update: %w", err)
	}
	occFinalState := jobs.OccurrenceFailed
	if outcome == jobs.AttemptCompleted {
		occFinalState = jobs.OccurrenceCompleted
	} else if outcome == jobs.AttemptCancelled {
		occFinalState = jobs.OccurrenceCancelled
	}
	if _, err := tx.ExecContext(ctx, `UPDATE job_occurrences SET state = ?, revision = revision + 1, updated_at = ? WHERE id = ?`, string(occFinalState), now, occID); err != nil {
		return err
	}

	return tx.Commit()
}

// ListReadyOccurrences retrieves a bounded page of ready occurrences (ADR 0006 §7.3).
func (s *Store) ListReadyOccurrences(ctx context.Context, limit int) ([]*jobs.JobOccurrence, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	query := `
	SELECT id, job_id, schedule_id, scheduled_for, occurrence_key, state, ready_at, cancel_epoch, revision
	FROM job_occurrences
	WHERE state = 'ready' AND ready_at <= ?
	ORDER BY ready_at ASC, id ASC
	LIMIT ?;
	`
	rows, err := s.db.QueryContext(ctx, query, time.Now().UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list ready occurrences: %w", err)
	}
	defer rows.Close()

	var occurrences []*jobs.JobOccurrence
	for rows.Next() {
		var occ jobs.JobOccurrence
		var schedID sql.NullString
		var stateStr string
		if err := rows.Scan(
			&occ.ID, &occ.JobID, &schedID, &occ.ScheduledFor, &occ.OccurrenceKey,
			&stateStr, &occ.ReadyAt, &occ.CancelEpoch, &occ.Revision,
		); err != nil {
			return nil, err
		}
		occ.ScheduleID = schedID.String
		occ.State = jobs.OccurrenceState(stateStr)
		occurrences = append(occurrences, &occ)
	}
	return occurrences, rows.Err()
}
