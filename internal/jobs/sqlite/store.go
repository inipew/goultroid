package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	ErrRevisionConflict   = errors.New("revision conflict: definition changed concurrently")
)

// Store provides persistence transactions for redesigned job definitions, schedules, occurrences, and attempts.
type Store struct {
	db *sql.DB
}

// NewStore creates a new Store instance.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// ListDefinitions restores durable definitions before recovery starts.
func (s *Store) ListDefinitions(ctx context.Context) ([]jobs.JobDefinition, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, scope_owner, quota_owner, handler_type, version, payload,
		       pool, class, timeout_ms, retry_policy, enabled, revision
		FROM job_definitions ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list job definitions: %w", err)
	}
	defer rows.Close()
	var definitions []jobs.JobDefinition
	for rows.Next() {
		var definition jobs.JobDefinition
		var timeoutMS int64
		var retryJSON string
		var enabled int
		if err := rows.Scan(&definition.ID, &definition.ScopeOwner, &definition.QuotaOwner,
			&definition.HandlerType, &definition.Version, &definition.Payload, &definition.Pool,
			&definition.Class, &timeoutMS, &retryJSON, &enabled, &definition.Revision); err != nil {
			return nil, fmt.Errorf("scan job definition: %w", err)
		}
		definition.Timeout = time.Duration(timeoutMS) * time.Millisecond
		definition.Enabled = enabled != 0
		if retryJSON != "" {
			if err := json.Unmarshal([]byte(retryJSON), &definition.RetryPolicy); err != nil {
				return nil, fmt.Errorf("decode retry policy for %s: %w", definition.ID, err)
			}
		}
		definition.Payload = append([]byte(nil), definition.Payload...)
		definitions = append(definitions, definition)
	}
	return definitions, rows.Err()
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

// UpdateDefinitionCAS updates a JobDefinition only if its revision still
// matches expectedRevision (compare-and-swap). Concurrent writers lose with
// ErrRevisionConflict instead of silently overwriting each other; the winner's
// revision is assigned back onto def.
func (s *Store) UpdateDefinitionCAS(ctx context.Context, def *jobs.JobDefinition, expectedRevision uint64) error {
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
	query := `
	UPDATE job_definitions SET
		scope_owner = ?, quota_owner = ?, handler_type = ?, version = ?,
		payload = ?, pool = ?, class = ?, timeout_ms = ?,
		retry_policy = ?, enabled = ?,
		revision = revision + 1, updated_at = ?
	WHERE id = ? AND revision = ?;
	`
	res, err := s.db.ExecContext(ctx, query,
		def.ScopeOwner, def.QuotaOwner, def.HandlerType, def.Version,
		def.Payload, def.Pool, def.Class, timeoutMs,
		string(retryPolicyBytes), enabledInt, now,
		def.ID, expectedRevision,
	)
	if err != nil {
		return fmt.Errorf("failed to update job definition %s: %w", def.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revision CAS rows affected: %w", err)
	}
	if affected == 0 {
		if _, gerr := s.GetDefinition(ctx, def.ID); gerr != nil {
			return gerr
		}
		return fmt.Errorf("%w: job definition %s", ErrRevisionConflict, def.ID)
	}
	def.Revision = expectedRevision + 1
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
// It is idempotent on occurrence_key: concurrent duplicate triggers resolve
// to the same canonical identity instead of failing, so at-most-one logical
// run exists per key. On a key conflict the passed occurrence is populated
// with the stored identity and nil is returned.
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
	if err == nil {
		return nil
	}
	// Resolve identity instead of failing: a duplicate key means another
	// trigger already materialized this logical run.
	existing, gerr := s.GetOccurrenceByKey(ctx, occ.OccurrenceKey)
	if gerr != nil {
		return fmt.Errorf("failed to materialize occurrence %s: %w", occ.ID, err)
	}
	*occ = *existing
	return nil
}

// GetOccurrence loads a JobOccurrence by ID.
func (s *Store) GetOccurrence(ctx context.Context, occurrenceID string) (*jobs.JobOccurrence, error) {
	query := `
	SELECT id, job_id, schedule_id, scheduled_for, occurrence_key, state, ready_at, cancel_epoch, revision
	FROM job_occurrences WHERE id = ?;
	`
	return s.scanOccurrence(ctx, query, occurrenceID)
}

// GetOccurrenceByKey loads a JobOccurrence by its idempotency key.
func (s *Store) GetOccurrenceByKey(ctx context.Context, occurrenceKey string) (*jobs.JobOccurrence, error) {
	query := `
	SELECT id, job_id, schedule_id, scheduled_for, occurrence_key, state, ready_at, cancel_epoch, revision
	FROM job_occurrences WHERE occurrence_key = ?;
	`
	return s.scanOccurrence(ctx, query, occurrenceKey)
}

func (s *Store) scanOccurrence(ctx context.Context, query, arg string) (*jobs.JobOccurrence, error) {
	var occ jobs.JobOccurrence
	var schedID sql.NullString
	var stateStr string
	err := s.db.QueryRowContext(ctx, query, arg).Scan(
		&occ.ID, &occ.JobID, &schedID, &occ.ScheduledFor, &occ.OccurrenceKey,
		&stateStr, &occ.ReadyAt, &occ.CancelEpoch, &occ.Revision,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOccurrenceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query job occurrence: %w", err)
	}
	occ.ScheduleID = schedID.String
	occ.State = jobs.OccurrenceState(stateStr)
	return &occ, nil
}

// CountAttempts returns the number of attempts recorded for an occurrence.
func (s *Store) CountAttempts(ctx context.Context, occurrenceID string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_attempts WHERE occurrence_id = ?`, occurrenceID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count job attempts: %w", err)
	}
	return n, nil
}

// LatestAttempt returns the highest-numbered attempt of an occurrence.
func (s *Store) LatestAttempt(ctx context.Context, occurrenceID string) (*jobs.JobAttempt, error) {
	// NOTE: time columns are selected directly (not wrapped in COALESCE):
	// the driver converts declared DATETIME columns to time.Time but
	// returns expressions as strings, which do not scan into time.Time.
	query := `
	SELECT id, occurrence_id, attempt_no, task_id, lease_epoch, lease_until, state,
	       started_at, finished_at, created_at,
	       COALESCE(result, ''), COALESCE(error, '')
	FROM job_attempts WHERE occurrence_id = ? ORDER BY attempt_no DESC LIMIT 1;
	`
	var a jobs.JobAttempt
	var stateStr string
	var started, finished sql.NullTime
	var created time.Time
	var result []byte
	var errStr string
	err := s.db.QueryRowContext(ctx, query, occurrenceID).Scan(
		&a.ID, &a.OccurrenceID, &a.AttemptNo, &a.TaskID, &a.LeaseEpoch,
		&a.LeaseUntil, &stateStr, &started, &finished, &created,
		&result, &errStr,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAttemptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query latest job attempt: %w", err)
	}
	a.State = jobs.AttemptState(stateStr)
	a.StartedAt = created
	if started.Valid {
		a.StartedAt = started.Time
	}
	a.FinishedAt = created
	if finished.Valid {
		a.FinishedAt = finished.Time
	}
	a.Result = result
	a.Error = errStr
	return &a, nil
}

// ListUnresolvedOccurrences returns a bounded page of dispatched occurrences
// whose final disposition is still unknown (crash/retry/recovery scan input).
func (s *Store) ListUnresolvedOccurrences(ctx context.Context, limit int) ([]*jobs.JobOccurrence, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	query := `
	SELECT id, job_id, schedule_id, scheduled_for, occurrence_key, state, ready_at, cancel_epoch, revision
	FROM job_occurrences
	WHERE state = 'dispatched'
	ORDER BY ready_at ASC, id ASC
	LIMIT ?;
	`
	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("list unresolved occurrences: %w", err)
	}
	defer rows.Close()
	var out []*jobs.JobOccurrence
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
		out = append(out, &occ)
	}
	return out, rows.Err()
}

// DeleteTerminalOccurrences removes terminal occurrences of one job older
// than before, bounding durable growth for high-frequency definitions.
// Attempts cascade via foreign keys where enforced; callers keep their own
// history elsewhere. Returns the number of occurrence rows removed.
func (s *Store) DeleteTerminalOccurrences(ctx context.Context, jobID string, before time.Time, limit int) (int64, error) {
	if jobID == "" {
		return 0, errors.New("job id is required for pruning")
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	res, err := s.db.ExecContext(ctx, `
	DELETE FROM job_occurrences
	WHERE id IN (
		SELECT id FROM job_occurrences
		WHERE job_id = ? AND state IN ('completed', 'failed', 'cancelled') AND updated_at < ?
		ORDER BY updated_at ASC
		LIMIT ?
	);`, jobID, before.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("prune terminal occurrences: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune rows affected: %w", err)
	}
	return n, nil
}

// FinalizeOccurrence closes a dispatched occurrence with a terminal state
// (failed or cancelled) once its attempt budget is exhausted or an operator
// intervenes. Completed and cancelled-by-commit occurrences are already final;
// finalizing them again with the same state is a no-op, any other transition
// from a terminal state is rejected.
func (s *Store) FinalizeOccurrence(ctx context.Context, occurrenceID string, state jobs.OccurrenceState) error {
	if state != jobs.OccurrenceFailed && state != jobs.OccurrenceCancelled {
		return fmt.Errorf("finalize occurrence %s: state must be failed or cancelled, got %s", occurrenceID, state)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin finalize occurrence: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE job_occurrences SET revision = revision WHERE id = ?`, occurrenceID); err != nil {
		return fmt.Errorf("acquire finalize writer intent: %w", err)
	}
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM job_occurrences WHERE id = ?`, occurrenceID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOccurrenceNotFound
		}
		return err
	}
	if current == string(state) {
		return tx.Commit()
	}
	if current != string(jobs.OccurrenceDispatched) {
		return fmt.Errorf("%w: occurrence %s is %s", ErrLeaseFencingLost, occurrenceID, current)
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `UPDATE job_occurrences SET state = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND state = 'dispatched'`, string(state), now, occurrenceID)
	if err != nil {
		return fmt.Errorf("finalize occurrence update: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("finalize rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: occurrence %s changed concurrently", ErrLeaseFencingLost, occurrenceID)
	}
	return tx.Commit()
}

// PrepareAttemptLease atomically creates an attempt and leases the occurrence under writer intent (ADR 0006 §7.3 & §7.4).
// The first attempt requires a ready occurrence; retries require a dispatched
// occurrence whose latest attempt is already terminal, so two live attempts
// for one occurrence can never exist. The occurrence transition is fenced on
// revision + cancel_epoch and the affected row is verified.
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

	// Count existing attempts to determine attempt_no
	var attemptCount int
	countQuery := `SELECT COUNT(*) FROM job_attempts WHERE occurrence_id = ?;`
	if err := tx.QueryRowContext(ctx, countQuery, occurrenceID).Scan(&attemptCount); err != nil {
		return nil, err
	}

	switch occState {
	case string(jobs.OccurrenceReady):
		if readyAt.After(time.Now().UTC()) {
			return nil, fmt.Errorf("%w: occurrence state is %s", ErrOccurrenceNotReady, occState)
		}
		if attemptCount != 0 {
			return nil, fmt.Errorf("%w: ready occurrence already has attempts", ErrLeaseFencingLost)
		}
	case string(jobs.OccurrenceDispatched):
		// Retry path: the previous attempt must be terminal, otherwise a live
		// attempt is still holding the lease. Stale non-terminal attempts
		// (crashed owner, unknown effect) are deliberately NOT overridden
		// here; recovery reports them instead of risking duplicate execution.
		var latestState string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM job_attempts WHERE occurrence_id = ? ORDER BY attempt_no DESC LIMIT 1`, occurrenceID).Scan(&latestState); err != nil {
			return nil, fmt.Errorf("latest attempt state: %w", err)
		}
		switch jobs.AttemptState(latestState) {
		case jobs.AttemptCompleted, jobs.AttemptFailed, jobs.AttemptTimedOut, jobs.AttemptCancelled, jobs.AttemptAbortedBeforeStart:
		default:
			return nil, fmt.Errorf("%w: previous attempt %s still active", ErrOccurrenceNotReady, latestState)
		}
	default:
		return nil, fmt.Errorf("%w: occurrence state is %s", ErrOccurrenceNotReady, occState)
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
	UPDATE job_occurrences SET state = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ? AND cancel_epoch = ? AND state IN ('ready', 'dispatched');
	`
	res, err := tx.ExecContext(ctx, updateOcc, string(jobs.OccurrenceDispatched), now, occurrenceID, occRev, cancelEpoch)
	if err != nil {
		return nil, fmt.Errorf("update occurrence state: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("prepare rows affected: %w", err)
	}
	if affected == 0 {
		return nil, fmt.Errorf("%w: occurrence %s changed concurrently", ErrLeaseFencingLost, occurrenceID)
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
	// Only completed and cancelled attempts close the occurrence. Failed,
	// timed-out, and aborted attempts leave it dispatched so the retry driver
	// (or recovery) can prepare a further attempt or finalize explicitly.
	// FinalizeOccurrence owns the failed/cancelled terminal transition.
	if outcome == jobs.AttemptCompleted || outcome == jobs.AttemptCancelled {
		occFinalState := jobs.OccurrenceCancelled
		if outcome == jobs.AttemptCompleted {
			occFinalState = jobs.OccurrenceCompleted
		}
		if _, err := tx.ExecContext(ctx, `UPDATE job_occurrences SET state = ?, revision = revision + 1, updated_at = ? WHERE id = ?`, string(occFinalState), now, occID); err != nil {
			return err
		}
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

// SaveSchedule saves or updates a JobSchedule.
func (s *Store) SaveSchedule(ctx context.Context, sched *jobs.JobSchedule) error {
	if sched == nil {
		return errors.New("job schedule is required")
	}
	if strings.TrimSpace(sched.ID) == "" || strings.TrimSpace(sched.JobID) == "" {
		return errors.New("schedule id and job id are required")
	}
	switch sched.Recurrence {
	case "once":
		if sched.Interval != 0 {
			return errors.New("one-shot schedule interval must be zero")
		}
	case "interval":
		if sched.Interval < time.Second {
			return errors.New("recurring schedule interval must be at least one second")
		}
	default:
		return fmt.Errorf("unsupported recurrence %q", sched.Recurrence)
	}
	if sched.NextDueAt.IsZero() {
		return errors.New("schedule next due time is required")
	}
	tz := sched.Timezone
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("invalid schedule timezone %q: %w", tz, err)
	}
	switch sched.MisfirePolicy {
	case "", jobs.MisfireRunOnce, jobs.MisfireSkip:
	default:
		return fmt.Errorf("unsupported misfire policy %q", sched.MisfirePolicy)
	}
	if sched.OverlapPolicy != "" && sched.OverlapPolicy != jobs.OverlapForbid {
		return fmt.Errorf("unsupported overlap policy %q", sched.OverlapPolicy)
	}
	query := `
	INSERT INTO job_schedules (
		id, job_id, recurrence, interval_seconds, timezone, next_due_at,
		misfire_policy, overlap_policy, enabled, revision, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		job_id = excluded.job_id,
		recurrence = excluded.recurrence,
		interval_seconds = excluded.interval_seconds,
		timezone = excluded.timezone,
		next_due_at = excluded.next_due_at,
		misfire_policy = excluded.misfire_policy,
		overlap_policy = excluded.overlap_policy,
		enabled = excluded.enabled,
		revision = revision + 1,
		updated_at = excluded.updated_at;
	`
	now := time.Now().UTC()
	enabledInt := 0
	if sched.Enabled {
		enabledInt = 1
	}
	intervalSec := int64(sched.Interval.Seconds())
	misfire := string(sched.MisfirePolicy)
	if misfire == "" {
		misfire = string(jobs.MisfireRunOnce)
	}
	overlap := string(sched.OverlapPolicy)
	if overlap == "" {
		overlap = string(jobs.OverlapForbid)
	}

	_, err := s.db.ExecContext(ctx, query,
		sched.ID, sched.JobID, sched.Recurrence, intervalSec, tz, sched.NextDueAt.UTC(),
		misfire, overlap, enabledInt, sched.Revision, now,
	)
	if err != nil {
		return fmt.Errorf("failed to save job schedule %s: %w", sched.ID, err)
	}
	return nil
}

// SkipDueSchedule advances a still-due schedule without materializing an
// occurrence. Writer intent serializes concurrent scheduler instances.
func (s *Store) SkipDueSchedule(ctx context.Context, scheduleID string, nextDue time.Time) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin skip due schedule: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE job_schedules SET revision = revision WHERE id = ?`, scheduleID); err != nil {
		return fmt.Errorf("acquire skipped schedule writer intent: %w", err)
	}
	var enabled int
	var current time.Time
	if err := tx.QueryRowContext(ctx, `SELECT enabled, next_due_at FROM job_schedules WHERE id = ?`, scheduleID).Scan(&enabled, &current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrScheduleNotFound
		}
		return err
	}
	if enabled == 0 || current.After(time.Now().UTC()) {
		return errors.New("schedule is not enabled or not due")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE job_schedules SET next_due_at = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND enabled = 1`, nextDue.UTC(), time.Now().UTC(), scheduleID); err != nil {
		return fmt.Errorf("advance skipped schedule: %w", err)
	}
	return tx.Commit()
}

// DisableSchedule prevents future materialization without deleting audit
// history or invalidating occurrences that have already been materialized.
func (s *Store) DisableSchedule(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE job_schedules
		SET enabled = 0, revision = revision + 1, updated_at = ?
		WHERE id = ? AND enabled = 1`, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("disable job schedule %s: %w", id, err)
	}
	_, err = result.RowsAffected()
	return err
}

// GetSchedule loads a JobSchedule by ID.
func (s *Store) GetSchedule(ctx context.Context, id string) (*jobs.JobSchedule, error) {
	query := `
	SELECT id, job_id, recurrence, interval_seconds, timezone, next_due_at,
	       misfire_policy, overlap_policy, enabled, revision
	FROM job_schedules WHERE id = ?;
	`
	row := s.db.QueryRowContext(ctx, query, id)
	var sched jobs.JobSchedule
	var intervalSec int64
	var misfireStr, overlapStr string
	var enabledInt int

	err := row.Scan(
		&sched.ID, &sched.JobID, &sched.Recurrence, &intervalSec, &sched.Timezone, &sched.NextDueAt,
		&misfireStr, &overlapStr, &enabledInt, &sched.Revision,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrScheduleNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query job schedule %s: %w", id, err)
	}
	sched.Interval = time.Duration(intervalSec) * time.Second
	sched.MisfirePolicy = jobs.MisfirePolicy(misfireStr)
	sched.OverlapPolicy = jobs.OverlapPolicy(overlapStr)
	sched.Enabled = enabledInt == 1
	return &sched, nil
}

func (s *Store) ListDueSchedules(ctx context.Context, now time.Time, limit int) ([]jobs.JobSchedule, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, job_id, recurrence, interval_seconds, timezone, next_due_at,
		       misfire_policy, overlap_policy, enabled, revision
		FROM job_schedules WHERE enabled = 1 AND next_due_at <= ?
		ORDER BY next_due_at, id LIMIT ?`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list due job schedules: %w", err)
	}
	defer rows.Close()
	var schedules []jobs.JobSchedule
	for rows.Next() {
		var schedule jobs.JobSchedule
		var intervalSeconds int64
		var misfire, overlap string
		var enabled int
		if err := rows.Scan(&schedule.ID, &schedule.JobID, &schedule.Recurrence, &intervalSeconds,
			&schedule.Timezone, &schedule.NextDueAt, &misfire, &overlap, &enabled, &schedule.Revision); err != nil {
			return nil, err
		}
		schedule.Interval = time.Duration(intervalSeconds) * time.Second
		schedule.MisfirePolicy = jobs.MisfirePolicy(misfire)
		schedule.OverlapPolicy = jobs.OverlapPolicy(overlap)
		schedule.Enabled = enabled != 0
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (s *Store) EarliestScheduleDue(ctx context.Context) (time.Time, bool, error) {
	// SQLite aggregate expressions lose the declared DATETIME column type and
	// modernc/sqlite consequently returns MIN(...) as text rather than time.Time.
	var raw any
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(next_due_at) FROM job_schedules WHERE enabled = 1`).Scan(&raw); err != nil {
		return time.Time{}, false, err
	}
	return parseAggregateTime(raw)
}

func parseAggregateTime(raw any) (time.Time, bool, error) {
	if raw == nil {
		return time.Time{}, false, nil
	}
	if value, ok := raw.(time.Time); ok {
		return value, true, nil
	}
	var value string
	switch typed := raw.(type) {
	case string:
		value = typed
	case []byte:
		value = string(typed)
	default:
		return time.Time{}, false, fmt.Errorf("unexpected aggregate time type %T", raw)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false, nil
	}
	if index := strings.Index(value, " m="); index >= 0 {
		value = value[:index]
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999 -0700 MST",
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("parse aggregate schedule time %q", value)
}

func (s *Store) CutoverActive(ctx context.Context) (bool, error) {
	var mode string
	if err := s.db.QueryRowContext(ctx, `SELECT mode FROM execution_runtime_state WHERE id = 1`).Scan(&mode); err != nil {
		return false, err
	}
	return mode == "redesigned", nil
}

// MaterializeDueSchedule atomically creates an occurrence from a due schedule and advances next_due_at (ADR 0006 §7.3).
func (s *Store) MaterializeDueSchedule(ctx context.Context, scheduleID string, nextDue time.Time) (*jobs.JobOccurrence, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin materialize due schedule tx: %w", err)
	}
	defer tx.Rollback()

	// Writer intent
	if _, err := tx.ExecContext(ctx, `UPDATE job_schedules SET revision = revision WHERE id = ?`, scheduleID); err != nil {
		return nil, fmt.Errorf("acquire schedule writer intent: %w", err)
	}

	var schedID, jobID, recurrence, tz, misfire, overlap string
	var intervalSec int64
	var nextDueAt time.Time
	var enabledInt int
	var rev uint64
	query := `
	SELECT id, job_id, recurrence, interval_seconds, timezone, next_due_at,
	       misfire_policy, overlap_policy, enabled, revision
	FROM job_schedules WHERE id = ?;
	`
	if err := tx.QueryRowContext(ctx, query, scheduleID).Scan(
		&schedID, &jobID, &recurrence, &intervalSec, &tz, &nextDueAt,
		&misfire, &overlap, &enabledInt, &rev,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrScheduleNotFound
		}
		return nil, err
	}

	now := time.Now().UTC()
	if enabledInt == 0 || nextDueAt.After(now) {
		return nil, errors.New("schedule is not enabled or not due")
	}

	// Overlap check
	if jobs.OverlapPolicy(overlap) == jobs.OverlapForbid {
		var activeCount int
		checkActive := `SELECT COUNT(*) FROM job_occurrences WHERE job_id = ? AND state IN ('ready', 'dispatched');`
		if err := tx.QueryRowContext(ctx, checkActive, jobID).Scan(&activeCount); err != nil {
			return nil, err
		}
		if activeCount > 0 {
			// A one-shot has no future slot: close it instead of leaving the same
			// due deadline enabled (which would make the scheduler spin). Recurring
			// schedules advance to the caller-computed future slot.
			enabledAfter := 1
			if recurrence == "once" {
				enabledAfter = 0
			}
			if _, err := tx.ExecContext(ctx, `UPDATE job_schedules SET next_due_at = ?, enabled = ?, revision = revision + 1, updated_at = ? WHERE id = ?`, nextDue.UTC(), enabledAfter, now, scheduleID); err != nil {
				return nil, err
			}
			return nil, tx.Commit()
		}
	}

	occurrenceKey := fmt.Sprintf("sched:%s:%s", scheduleID, nextDueAt.UTC().Format(time.RFC3339Nano))
	occID := fmt.Sprintf("occ:%s:%d", scheduleID, nextDueAt.UTC().UnixNano())
	insertOcc := `
	INSERT INTO job_occurrences (
		id, job_id, schedule_id, scheduled_for, occurrence_key, state, ready_at, cancel_epoch, revision, updated_at
	) VALUES (?, ?, ?, ?, ?, 'ready', ?, 0, 1, ?)
	ON CONFLICT(occurrence_key) DO UPDATE SET updated_at = excluded.updated_at;
	`
	if _, err := tx.ExecContext(ctx, insertOcc, occID, jobID, scheduleID, nextDueAt, occurrenceKey, now, now); err != nil {
		return nil, fmt.Errorf("materialize occurrence: %w", err)
	}

	// Advance recurring schedules; one-shot schedules are disabled atomically.
	enabledAfter := 1
	if recurrence == "once" {
		enabledAfter = 0
	}
	if _, err := tx.ExecContext(ctx, `UPDATE job_schedules SET next_due_at = ?, enabled = ?, revision = revision + 1, updated_at = ? WHERE id = ?`, nextDue.UTC(), enabledAfter, now, scheduleID); err != nil {
		return nil, fmt.Errorf("advance schedule next due: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &jobs.JobOccurrence{
		ID:            occID,
		JobID:         jobID,
		ScheduleID:    scheduleID,
		ScheduledFor:  nextDueAt,
		OccurrenceKey: occurrenceKey,
		State:         jobs.OccurrenceReady,
		ReadyAt:       now,
		Revision:      1,
	}, nil
}

// CancelOccurrence cancels a job occurrence, bumps its cancel epoch to fence in-flight attempts,
// and records an outbox event (ADR 0006 §7.2).
func (s *Store) CancelOccurrence(ctx context.Context, occurrenceID string, reason string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin cancel occurrence tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE job_occurrences SET revision = revision WHERE id = ?`, occurrenceID); err != nil {
		return fmt.Errorf("acquire cancel writer intent: %w", err)
	}

	var state string
	var cancelEpoch, rev uint64
	if err := tx.QueryRowContext(ctx, `SELECT state, cancel_epoch, revision FROM job_occurrences WHERE id = ?`, occurrenceID).Scan(&state, &cancelEpoch, &rev); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOccurrenceNotFound
		}
		return err
	}

	if state == string(jobs.OccurrenceCompleted) || state == string(jobs.OccurrenceFailed) || state == string(jobs.OccurrenceCancelled) {
		return nil // Already terminal
	}

	now := time.Now().UTC()
	newState := jobs.OccurrenceCancelled
	newCancelEpoch := cancelEpoch + 1

	if _, err := tx.ExecContext(ctx, `UPDATE job_occurrences SET state = ?, cancel_epoch = ?, revision = revision + 1, updated_at = ? WHERE id = ?`, string(newState), newCancelEpoch, now, occurrenceID); err != nil {
		return fmt.Errorf("update occurrence cancel: %w", err)
	}

	// Record outbox event for cancellation
	eventID := fmt.Sprintf("outbox:cancel:%s:%d", occurrenceID, newCancelEpoch)
	payload := []byte(fmt.Sprintf(`{"occurrence_id":%q,"reason":%q,"cancel_epoch":%d}`, occurrenceID, reason, newCancelEpoch))
	insertOutbox := `
	INSERT INTO job_outbox (event_id, occurrence_id, kind, payload, committed_at, delivery_state)
	VALUES (?, ?, 'occurrence_cancelled', ?, ?, 'pending');
	`
	if _, err := tx.ExecContext(ctx, insertOutbox, eventID, occurrenceID, payload, now); err != nil {
		return fmt.Errorf("insert cancel outbox event: %w", err)
	}

	return tx.Commit()
}

// RecordOutboxEvent inserts an outbox event for asynchronous reliable delivery (ADR 0006 §7.1).
func (s *Store) RecordOutboxEvent(ctx context.Context, eventID, occurrenceID, kind string, payload []byte) error {
	query := `
	INSERT INTO job_outbox (event_id, occurrence_id, kind, payload, committed_at, delivery_state)
	VALUES (?, ?, ?, ?, ?, 'pending');
	`
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, query, eventID, occurrenceID, kind, payload, now)
	if err != nil {
		return fmt.Errorf("record outbox event: %w", err)
	}
	return nil
}

// ListPendingOutbox returns a bounded ordered delivery batch.
func (s *Store) ListPendingOutbox(ctx context.Context, limit int) ([]jobs.OutboxEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_id, occurrence_id, kind, payload, committed_at
		FROM job_outbox WHERE delivery_state = 'pending'
		ORDER BY committed_at, event_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending job outbox: %w", err)
	}
	defer rows.Close()
	events := make([]jobs.OutboxEvent, 0, limit)
	for rows.Next() {
		var event jobs.OutboxEvent
		if err := rows.Scan(&event.ID, &event.OccurrenceID, &event.Kind, &event.Payload, &event.CommittedAt); err != nil {
			return nil, fmt.Errorf("scan pending job outbox: %w", err)
		}
		event.Payload = append([]byte(nil), event.Payload...)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) MarkOutboxDelivered(ctx context.Context, eventID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE job_outbox SET delivery_state = 'delivered' WHERE event_id = ? AND delivery_state = 'pending'`, eventID)
	if err != nil {
		return fmt.Errorf("mark job outbox delivered: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("job outbox event is not pending")
	}
	return nil
}

// CommitAttemptResultWithOutbox atomically commits an attempt result and enqueues an outbox event.
func (s *Store) CommitAttemptResultWithOutbox(ctx context.Context, attemptID string, leaseEpoch uint64, outcome jobs.AttemptState, result []byte, errStr string, outboxEventID string, outboxKind string, outboxPayload []byte) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin commit attempt result with outbox: %w", err)
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
			return tx.Commit()
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
	// Same retry-aware finalization as CommitAttemptResult: only completed
	// and cancelled attempts close the occurrence.
	if outcome == jobs.AttemptCompleted || outcome == jobs.AttemptCancelled {
		occFinalState := jobs.OccurrenceCancelled
		if outcome == jobs.AttemptCompleted {
			occFinalState = jobs.OccurrenceCompleted
		}
		if _, err := tx.ExecContext(ctx, `UPDATE job_occurrences SET state = ?, revision = revision + 1, updated_at = ? WHERE id = ?`, string(occFinalState), now, occID); err != nil {
			return err
		}
	}

	if outboxEventID != "" && outboxKind != "" {
		insertOutbox := `
		INSERT INTO job_outbox (event_id, occurrence_id, kind, payload, committed_at, delivery_state)
		VALUES (?, ?, ?, ?, ?, 'pending');
		`
		if _, err := tx.ExecContext(ctx, insertOutbox, outboxEventID, occID, outboxKind, outboxPayload, now); err != nil {
			return fmt.Errorf("insert completion outbox: %w", err)
		}
	}

	return tx.Commit()
}
