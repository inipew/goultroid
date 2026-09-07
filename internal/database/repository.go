package database

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

// SudoUser represents a registered sudo user.
type SudoUser struct {
	UserID  int64     `json:"user_id"`
	AddedAt time.Time `json:"added_at"`
	AddedBy int64     `json:"added_by"`
}

// Note represents a saved note for a specific chat.
type Note struct {
	ChatID    int64     `json:"chat_id"`
	Name      string    `json:"name"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AFK represents the AFK state of a user.
type AFK struct {
	UserID int64     `json:"user_id"`
	IsAFK  bool      `json:"is_afk"`
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}

// Filter represents a chat-specific auto-reply keyword filter.
type Filter struct {
	ChatID    int64     `json:"chat_id"`
	Keyword   string    `json:"keyword"`
	ReplyText string    `json:"reply_text"`
	CreatedAt time.Time `json:"created_at"`
}

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

// SettingItem represents a persisted generic configuration entry.
type SettingItem struct {
	ScopeType string    `json:"scope_type"` // "global", "chat", "user"
	ScopeID   int64     `json:"scope_id"`   // 0 for global, chatID or userID
	Namespace string    `json:"namespace"`  // e.g. "core", "afk", "pmpermit"
	Key       string    `json:"key"`        // e.g. "prefix", "cooldown"
	ValueType string    `json:"value_type"` // "bool", "int", "string", "duration", "enum"
	Value     string    `json:"value"`      // string serialized value
	UpdatedBy int64     `json:"updated_by"` // user ID of updater
	UpdatedAt time.Time `json:"updated_at"`
}

// SettingChangeRecord represents an audit history log entry for setting mutations.
type SettingChangeRecord struct {
	ID        int64     `json:"id"`
	ScopeType string    `json:"scope_type"`
	ScopeID   int64     `json:"scope_id"`
	Namespace string    `json:"namespace"`
	Key       string    `json:"key"`
	OldVal    string    `json:"old_val"`
	NewVal    string    `json:"new_val"`
	ChangedBy int64     `json:"changed_by"`
	ChangedAt time.Time `json:"changed_at"`
}

// Repository defines data access methods for GoUltroid.
type Repository interface {
	// Sudo
	GetSudoUsers(ctx context.Context) ([]SudoUser, error)
	AddSudoUser(ctx context.Context, userID, addedBy int64) error
	RemoveSudoUser(ctx context.Context, userID int64) error
	IsSudoUser(ctx context.Context, userID int64) (bool, error)

	// Notes
	SaveNote(ctx context.Context, chatID int64, name, content string) error
	GetNote(ctx context.Context, chatID int64, name string) (*Note, error)
	ListNotes(ctx context.Context, chatID int64) ([]string, error)
	DeleteNote(ctx context.Context, chatID int64, name string) error

	// AFK
	SetAFK(ctx context.Context, userID int64, isAFK bool, reason string) error
	GetAFK(ctx context.Context, userID int64) (*AFK, error)

	// Filters
	SaveFilter(ctx context.Context, chatID int64, keyword, replyText string) error
	GetFilter(ctx context.Context, chatID int64, keyword string) (*Filter, error)
	ListFilters(ctx context.Context, chatID int64) ([]Filter, error)
	DeleteFilter(ctx context.Context, chatID int64, keyword string) error

	// Scheduled Jobs
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

	// Job History
	RecordJobRun(ctx context.Context, entry *JobHistoryEntry) error
	GetJobHistory(ctx context.Context, jobID int64, limit int) ([]JobHistoryEntry, error)

	// Peer Metadata
	SavePeerEntity(ctx context.Context, prefix string, id int64, username, phone, firstName, lastName, title string) error
	FindPeerByUsername(ctx context.Context, username string) (prefix string, id int64, accessHash int64, found bool, err error)

	// Blacklist
	AddBlacklist(ctx context.Context, chatID int64, word string) error
	RemoveBlacklist(ctx context.Context, chatID int64, word string) error
	ListBlacklists(ctx context.Context, chatID int64) ([]string, error)

	// Generic Settings
	GetSetting(ctx context.Context, scopeType string, scopeID int64, namespace, key string) (*SettingItem, error)
	SetSetting(ctx context.Context, item *SettingItem) error
	SetSettingsBatch(ctx context.Context, items []*SettingItem) error
	DeleteSetting(ctx context.Context, scopeType string, scopeID int64, namespace, key string) error
	ListSettings(ctx context.Context, scopeType string, scopeID int64, namespace string) ([]SettingItem, error)
	GetSettingHistory(ctx context.Context, namespace, key string, limit int) ([]SettingChangeRecord, error)
}

// Ensure DB implements Repository.
var _ Repository = (*DB)(nil)

// =================== Sudo User Methods ===================

func (d *DB) GetSudoUsers(ctx context.Context) ([]SudoUser, error) {
	rows, err := d.QueryContext(ctx, "SELECT user_id, added_at, added_by FROM sudo_users ORDER BY user_id ASC")
	if err != nil {
		return nil, fmt.Errorf("failed to query sudo users: %w", err)
	}
	defer rows.Close()

	var users []SudoUser
	for rows.Next() {
		var u SudoUser
		if err := rows.Scan(&u.UserID, &u.AddedAt, &u.AddedBy); err != nil {
			return nil, fmt.Errorf("failed to scan sudo user: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return users, nil
}

func (d *DB) AddSudoUser(ctx context.Context, userID, addedBy int64) error {
	query := `
	INSERT INTO sudo_users (user_id, added_at, added_by)
	VALUES (?, ?, ?)
	ON CONFLICT(user_id) DO UPDATE SET added_at = excluded.added_at, added_by = excluded.added_by
	`
	_, err := d.ExecContext(ctx, query, userID, time.Now().UTC(), addedBy)
	if err != nil {
		return fmt.Errorf("failed to add sudo user: %w", err)
	}
	return nil
}

func (d *DB) RemoveSudoUser(ctx context.Context, userID int64) error {
	res, err := d.ExecContext(ctx, "DELETE FROM sudo_users WHERE user_id = ?", userID)
	if err != nil {
		return fmt.Errorf("failed to remove sudo user: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("user is not in sudo list")
	}
	return nil
}

func (d *DB) IsSudoUser(ctx context.Context, userID int64) (bool, error) {
	var exists bool
	err := d.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sudo_users WHERE user_id = ?)", userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check sudo user: %w", err)
	}
	return exists, nil
}

// =================== Notes Methods ===================

func (d *DB) SaveNote(ctx context.Context, chatID int64, name, content string) error {
	now := time.Now().UTC()
	query := `
	INSERT INTO notes (chat_id, name, content, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?)
	ON CONFLICT(chat_id, name) DO UPDATE SET content = excluded.content, updated_at = excluded.updated_at
	`
	_, err := d.ExecContext(ctx, query, chatID, name, content, now, now)
	if err != nil {
		return fmt.Errorf("failed to save note: %w", err)
	}
	return nil
}

func (d *DB) GetNote(ctx context.Context, chatID int64, name string) (*Note, error) {
	query := "SELECT chat_id, name, content, created_at, updated_at FROM notes WHERE chat_id = ? AND name = ?"
	row := d.QueryRowContext(ctx, query, chatID, name)

	var n Note
	err := row.Scan(&n.ChatID, &n.Name, &n.Content, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // not found
		}
		return nil, fmt.Errorf("failed to get note: %w", err)
	}
	return &n, nil
}

func (d *DB) ListNotes(ctx context.Context, chatID int64) ([]string, error) {
	rows, err := d.QueryContext(ctx, "SELECT name FROM notes WHERE chat_id = ? ORDER BY name ASC", chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list notes: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan note name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return names, nil
}

func (d *DB) DeleteNote(ctx context.Context, chatID int64, name string) error {
	res, err := d.ExecContext(ctx, "DELETE FROM notes WHERE chat_id = ? AND name = ?", chatID, name)
	if err != nil {
		return fmt.Errorf("failed to delete note: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("note not found")
	}
	return nil
}

// =================== AFK Methods ===================

func (d *DB) SetAFK(ctx context.Context, userID int64, isAFK bool, reason string) error {
	now := time.Now().UTC()
	if isAFK {
		query := `
		INSERT INTO afk_status (user_id, is_afk, reason, since)
		VALUES (?, 1, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET is_afk = 1, reason = excluded.reason, since = excluded.since
		`
		_, err := d.ExecContext(ctx, query, userID, reason, now)
		if err != nil {
			return fmt.Errorf("failed to update afk status: %w", err)
		}
		return nil
	}

	// Atomic deactivate: update is_afk = 0 without overwriting the original since timestamp
	query := `UPDATE afk_status SET is_afk = 0 WHERE user_id = ? AND is_afk = 1`
	res, err := d.ExecContext(ctx, query, userID)
	if err != nil {
		return fmt.Errorf("failed to deactivate afk status: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		insertQuery := `
		INSERT INTO afk_status (user_id, is_afk, reason, since)
		VALUES (?, 0, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET is_afk = 0
		`
		_, _ = d.ExecContext(ctx, insertQuery, userID, reason, now)
	}
	return nil
}

func (d *DB) GetAFK(ctx context.Context, userID int64) (*AFK, error) {
	query := "SELECT user_id, is_afk, reason, since FROM afk_status WHERE user_id = ?"
	row := d.QueryRowContext(ctx, query, userID)

	var a AFK
	err := row.Scan(&a.UserID, &a.IsAFK, &a.Reason, &a.Since)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // not found
		}
		return nil, fmt.Errorf("failed to get afk status: %w", err)
	}
	return &a, nil
}

// =================== Filter Methods ===================

func (d *DB) SaveFilter(ctx context.Context, chatID int64, keyword, replyText string) error {
	query := `
	INSERT INTO filters (chat_id, keyword, reply_text, created_at)
	VALUES (?, ?, ?, ?)
	ON CONFLICT(chat_id, keyword) DO UPDATE SET
		reply_text = excluded.reply_text,
		created_at = excluded.created_at;
	`
	_, err := d.ExecContext(ctx, query, chatID, strings.ToLower(keyword), replyText, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("failed to save filter: %w", err)
	}
	return nil
}

func (d *DB) GetFilter(ctx context.Context, chatID int64, keyword string) (*Filter, error) {
	query := "SELECT chat_id, keyword, reply_text, created_at FROM filters WHERE chat_id = ? AND keyword = ?"
	row := d.QueryRowContext(ctx, query, chatID, strings.ToLower(keyword))

	var f Filter
	if err := row.Scan(&f.ChatID, &f.Keyword, &f.ReplyText, &f.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get filter: %w", err)
	}
	return &f, nil
}

func (d *DB) ListFilters(ctx context.Context, chatID int64) ([]Filter, error) {
	query := "SELECT chat_id, keyword, reply_text, created_at FROM filters WHERE chat_id = ? ORDER BY keyword ASC"
	rows, err := d.QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list filters: %w", err)
	}
	defer rows.Close()

	var filters []Filter
	for rows.Next() {
		var f Filter
		if err := rows.Scan(&f.ChatID, &f.Keyword, &f.ReplyText, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan filter: %w", err)
		}
		filters = append(filters, f)
	}
	return filters, rows.Err()
}

func (d *DB) DeleteFilter(ctx context.Context, chatID int64, keyword string) error {
	query := "DELETE FROM filters WHERE chat_id = ? AND keyword = ?"
	res, err := d.ExecContext(ctx, query, chatID, strings.ToLower(keyword))
	if err != nil {
		return fmt.Errorf("failed to delete filter: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("filter not found")
	}
	return nil
}

// =================== Scheduled Job Methods ===================

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

func (d *DB) CreateScheduledJob(ctx context.Context, job *ScheduledJob) (*ScheduledJob, error) {
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
	res, err := d.ExecContext(ctx, query,
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

func (d *DB) GetScheduledJob(ctx context.Context, id int64) (*ScheduledJob, error) {
	query := `SELECT ` + scheduledJobColumns + ` FROM scheduled_jobs WHERE id = ?`
	row := d.QueryRowContext(ctx, query, id)
	job, err := scanScheduledJob(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get scheduled job: %w", err)
	}
	return job, nil
}

func (d *DB) ListScheduledJobs(ctx context.Context, chatID int64) ([]ScheduledJob, error) {
	query := `SELECT ` + scheduledJobColumns + ` FROM scheduled_jobs WHERE chat_id = ? ORDER BY next_run_at ASC`
	rows, err := d.QueryContext(ctx, query, chatID)
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

func (d *DB) ListDueScheduledJobs(ctx context.Context, before time.Time) ([]ScheduledJob, error) {
	query := `SELECT ` + scheduledJobColumns + `
		FROM scheduled_jobs
		WHERE (status = 'pending' AND next_run_at <= ?)
		   OR (status = 'running' AND lease_until IS NOT NULL AND lease_until < ? AND next_run_at <= ?)
		ORDER BY next_run_at ASC`
	rows, err := d.QueryContext(ctx, query, before, before, before)
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

func (d *DB) ClaimDueScheduledJobs(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]ScheduledJob, error) {
	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		jobs, err := d.claimDueScheduledJobsOnce(ctx, now, limit, lease)
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

func (d *DB) claimDueScheduledJobsOnce(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]ScheduledJob, error) {
	if limit <= 0 {
		limit = 10
	}
	if lease <= 0 {
		lease = 90 * time.Second
	}

	tx, err := d.BeginTx(ctx, &sql.TxOptions{})
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

func (d *DB) CompleteScheduledJob(ctx context.Context, id int64, claimToken string, durationMs int64, now time.Time) error {
	tx, err := d.BeginTx(ctx, &sql.TxOptions{})
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

func (d *DB) FailScheduledJob(ctx context.Context, id int64, claimToken string, lastError string, durationMs int64, retryDelay time.Duration, isPermanent bool, now time.Time) error {
	tx, err := d.BeginTx(ctx, &sql.TxOptions{})
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

func (d *DB) UpdateScheduledJobNextRun(ctx context.Context, id int64, nextRun time.Time) error {
	query := "UPDATE scheduled_jobs SET next_run_at = ? WHERE id = ?"
	res, err := d.ExecContext(ctx, query, nextRun, id)
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

func (d *DB) RecordJobFailure(ctx context.Context, id int64, lastError string) error {
	query := "UPDATE scheduled_jobs SET attempt_count = attempt_count + 1, last_error = ? WHERE id = ?"
	res, err := d.ExecContext(ctx, query, lastError, id)
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

func (d *DB) DeleteScheduledJob(ctx context.Context, id int64) error {
	query := "DELETE FROM scheduled_jobs WHERE id = ?"
	res, err := d.ExecContext(ctx, query, id)
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

// =================== Blacklist Methods ===================

func (d *DB) AddBlacklist(ctx context.Context, chatID int64, word string) error {
	word = strings.ToLower(strings.TrimSpace(word))
	if word == "" {
		return errors.New("word cannot be empty")
	}

	query := `INSERT INTO blacklists (chat_id, word, created_at)
	          VALUES (?, ?, ?)
	          ON CONFLICT(chat_id, word) DO UPDATE SET created_at = excluded.created_at`
	_, err := d.ExecContext(ctx, query, chatID, word, time.Now())
	if err != nil {
		return fmt.Errorf("failed to add blacklist word: %w", err)
	}
	return nil
}

func (d *DB) RemoveBlacklist(ctx context.Context, chatID int64, word string) error {
	word = strings.ToLower(strings.TrimSpace(word))
	query := "DELETE FROM blacklists WHERE chat_id = ? AND word = ?"
	res, err := d.ExecContext(ctx, query, chatID, word)
	if err != nil {
		return fmt.Errorf("failed to remove blacklist word: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("word not found in blacklist")
	}
	return nil
}

func (d *DB) ListBlacklists(ctx context.Context, chatID int64) ([]string, error) {
	query := "SELECT word FROM blacklists WHERE chat_id = ? ORDER BY word ASC"
	rows, err := d.QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list blacklists: %w", err)
	}
	defer rows.Close()

	var words []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, fmt.Errorf("failed to scan blacklist word: %w", err)
		}
		words = append(words, w)
	}
	return words, rows.Err()
}

// =================== Job History Methods ===================

// RecordJobRun inserts a history entry for a single scheduler job execution attempt.
func (d *DB) RecordJobRun(ctx context.Context, entry *JobHistoryEntry) error {
	if entry == nil {
		return fmt.Errorf("job history entry cannot be nil")
	}
	query := `
		INSERT INTO scheduled_job_history (job_id, ran_at, duration_ms, success, error_msg)
		VALUES (?, ?, ?, ?, ?)`
	if _, err := d.ExecContext(ctx, query, entry.JobID, entry.RanAt, entry.DurationMs, entry.Success, entry.ErrorMsg); err != nil {
		return fmt.Errorf("failed to record job run: %w", err)
	}
	return nil
}

// GetJobHistory returns the most recent execution history entries for a scheduled job,
// ordered newest-first. Limit ≤ 0 defaults to 20.
func (d *DB) GetJobHistory(ctx context.Context, jobID int64, limit int) ([]JobHistoryEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT id, job_id, ran_at, duration_ms, success, error_msg
		FROM scheduled_job_history
		WHERE job_id = ?
		ORDER BY ran_at DESC
		LIMIT ?`
	rows, err := d.QueryContext(ctx, query, jobID, limit)
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
func (d *DB) RenewJobLease(ctx context.Context, id int64, claimToken string, extension time.Duration, now time.Time) error {
	newLease := now.Add(extension)
	query := `
		UPDATE scheduled_jobs
		SET lease_until = ?
		WHERE id = ? AND status = 'running' AND claim_token = ?`
	res, err := d.ExecContext(ctx, query, newLease, id, claimToken)
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

// =================== Peer Metadata Entity Methods ===================

// PeerEntity stores metadata for Telegram peers (users, chats, channels).
type PeerEntity struct {
	Prefix    string    `json:"prefix"`
	ID        int64     `json:"id"`
	Username  string    `json:"username,omitempty"`
	Phone     string    `json:"phone,omitempty"`
	FirstName string    `json:"first_name,omitempty"`
	LastName  string    `json:"last_name,omitempty"`
	Title     string    `json:"title,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SavePeerEntity creates or updates entity metadata for a peer (user or channel/chat).
func (d *DB) SavePeerEntity(ctx context.Context, prefix string, id int64, username, phone, firstName, lastName, title string) error {
	query := `
		INSERT INTO peers_entities (prefix, id, username, phone, first_name, last_name, title, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(prefix, id) DO UPDATE SET
			username = CASE WHEN excluded.username != '' THEN excluded.username ELSE peers_entities.username END,
			phone = CASE WHEN excluded.phone != '' THEN excluded.phone ELSE peers_entities.phone END,
			first_name = CASE WHEN excluded.first_name != '' THEN excluded.first_name ELSE peers_entities.first_name END,
			last_name = CASE WHEN excluded.last_name != '' THEN excluded.last_name ELSE peers_entities.last_name END,
			title = CASE WHEN excluded.title != '' THEN excluded.title ELSE peers_entities.title END,
			updated_at = excluded.updated_at`
	_, err := d.ExecContext(ctx, query, prefix, id, strings.TrimPrefix(username, "@"), phone, firstName, lastName, title, time.Now())
	if err != nil {
		return fmt.Errorf("failed to save peer entity (%s:%d): %w", prefix, id, err)
	}
	return nil
}

// FindPeerByUsername searches local SQLite cache for a peer by its username (case-insensitive)
// and returns its prefix, id, and access hash from peers_storage.
func (d *DB) FindPeerByUsername(ctx context.Context, username string) (string, int64, int64, bool, error) {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" {
		return "", 0, 0, false, nil
	}
	query := `
		SELECT e.prefix, e.id, COALESCE(s.access_hash, 0)
		FROM peers_entities e
		LEFT JOIN peers_storage s ON e.prefix = s.prefix AND e.id = s.id
		WHERE e.username = ? COLLATE NOCASE
		LIMIT 1`
	var prefix string
	var id int64
	var accessHash int64
	err := d.QueryRowContext(ctx, query, username).Scan(&prefix, &id, &accessHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, 0, false, nil
		}
		return "", 0, 0, false, fmt.Errorf("failed to find peer by username %q: %w", username, err)
	}
	return prefix, id, accessHash, true, nil
}

// =================== Settings Methods ===================

// GetSetting retrieves a setting by its scope, namespace, and key.
// If the setting does not exist, it returns (nil, nil).
func (d *DB) GetSetting(ctx context.Context, scopeType string, scopeID int64, namespace, key string) (*SettingItem, error) {
	query := `
		SELECT scope_type, scope_id, namespace, key, value_type, value, updated_by, updated_at
		FROM settings
		WHERE scope_type = ? AND scope_id = ? AND namespace = ? AND key = ?`
	var item SettingItem
	err := d.QueryRowContext(ctx, query, scopeType, scopeID, namespace, key).Scan(
		&item.ScopeType,
		&item.ScopeID,
		&item.Namespace,
		&item.Key,
		&item.ValueType,
		&item.Value,
		&item.UpdatedBy,
		&item.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get setting (%s:%d:%s:%s): %w", scopeType, scopeID, namespace, key, err)
	}
	return &item, nil
}

// SetSetting creates or updates a setting and appends a record to setting_changes audit log in a transaction.
func (d *DB) SetSetting(ctx context.Context, item *SettingItem) error {
	if item == nil {
		return errors.New("item cannot be nil")
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = time.Now().UTC()
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Query old value if exists
	var oldVal string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE scope_type = ? AND scope_id = ? AND namespace = ? AND key = ?`,
		item.ScopeType, item.ScopeID, item.Namespace, item.Key).Scan(&oldVal)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to read previous setting value: %w", err)
	}

	// Upsert setting
	upsertQuery := `
		INSERT INTO settings (scope_type, scope_id, namespace, key, value_type, value, updated_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope_type, scope_id, namespace, key) DO UPDATE SET
			value_type = excluded.value_type,
			value = excluded.value,
			updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`
	_, err = tx.ExecContext(ctx, upsertQuery,
		item.ScopeType, item.ScopeID, item.Namespace, item.Key, item.ValueType, item.Value, item.UpdatedBy, item.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to upsert setting: %w", err)
	}

	// Insert audit record
	changeQuery := `
		INSERT INTO setting_changes (scope_type, scope_id, namespace, key, old_val, new_val, changed_by, changed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = tx.ExecContext(ctx, changeQuery,
		item.ScopeType, item.ScopeID, item.Namespace, item.Key, oldVal, item.Value, item.UpdatedBy, item.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to record setting change audit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit setting transaction: %w", err)
	}
	return nil
}

// SetSettingsBatch creates or updates multiple settings atomically within a single transaction,
// recording audit entries for each change.
func (d *DB) SetSettingsBatch(ctx context.Context, items []*SettingItem) error {
	if len(items) == 0 {
		return nil
	}
	now := time.Now().UTC()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin batch settings transaction: %w", err)
	}
	defer tx.Rollback()

	selectStmt, err := tx.PrepareContext(ctx, `SELECT value FROM settings WHERE scope_type = ? AND scope_id = ? AND namespace = ? AND key = ?`)
	if err != nil {
		return fmt.Errorf("prepare select statement: %w", err)
	}
	defer selectStmt.Close()

	upsertStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO settings (scope_type, scope_id, namespace, key, value_type, value, updated_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope_type, scope_id, namespace, key) DO UPDATE SET
			value_type = excluded.value_type,
			value = excluded.value,
			updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`)
	if err != nil {
		return fmt.Errorf("prepare upsert statement: %w", err)
	}
	defer upsertStmt.Close()

	changeStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO setting_changes (scope_type, scope_id, namespace, key, old_val, new_val, changed_by, changed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare audit statement: %w", err)
	}
	defer changeStmt.Close()

	for _, item := range items {
		if item == nil {
			continue
		}
		if item.UpdatedAt.IsZero() {
			item.UpdatedAt = now
		}

		var oldVal string
		err := selectStmt.QueryRowContext(ctx, item.ScopeType, item.ScopeID, item.Namespace, item.Key).Scan(&oldVal)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failed to read previous setting value for %s/%s: %w", item.Namespace, item.Key, err)
		}

		_, err = upsertStmt.ExecContext(ctx,
			item.ScopeType, item.ScopeID, item.Namespace, item.Key, item.ValueType, item.Value, item.UpdatedBy, item.UpdatedAt)
		if err != nil {
			return fmt.Errorf("failed to upsert setting %s/%s: %w", item.Namespace, item.Key, err)
		}

		_, err = changeStmt.ExecContext(ctx,
			item.ScopeType, item.ScopeID, item.Namespace, item.Key, oldVal, item.Value, item.UpdatedBy, item.UpdatedAt)
		if err != nil {
			return fmt.Errorf("failed to record setting change audit for %s/%s: %w", item.Namespace, item.Key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit batch settings transaction: %w", err)
	}
	return nil
}

// DeleteSetting removes a setting and records the deletion in the audit log in a transaction.
func (d *DB) DeleteSetting(ctx context.Context, scopeType string, scopeID int64, namespace, key string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var oldVal string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE scope_type = ? AND scope_id = ? AND namespace = ? AND key = ?`,
		scopeType, scopeID, namespace, key).Scan(&oldVal)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // Nothing to delete
		}
		return fmt.Errorf("failed to read setting before deletion: %w", err)
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM settings WHERE scope_type = ? AND scope_id = ? AND namespace = ? AND key = ?`,
		scopeType, scopeID, namespace, key)
	if err != nil {
		return fmt.Errorf("failed to delete setting: %w", err)
	}

	changeQuery := `
		INSERT INTO setting_changes (scope_type, scope_id, namespace, key, old_val, new_val, changed_by, changed_at)
		VALUES (?, ?, ?, ?, ?, '', 0, ?)`
	_, err = tx.ExecContext(ctx, changeQuery, scopeType, scopeID, namespace, key, oldVal, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("failed to record setting deletion audit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit setting deletion: %w", err)
	}
	return nil
}

// ListSettings returns all settings for a scope, optionally filtered by namespace (if non-empty).
func (d *DB) ListSettings(ctx context.Context, scopeType string, scopeID int64, namespace string) ([]SettingItem, error) {
	var rows *sql.Rows
	var err error
	if namespace != "" {
		query := `
			SELECT scope_type, scope_id, namespace, key, value_type, value, updated_by, updated_at
			FROM settings
			WHERE scope_type = ? AND scope_id = ? AND namespace = ?
			ORDER BY key ASC`
		rows, err = d.QueryContext(ctx, query, scopeType, scopeID, namespace)
	} else {
		query := `
			SELECT scope_type, scope_id, namespace, key, value_type, value, updated_by, updated_at
			FROM settings
			WHERE scope_type = ? AND scope_id = ?
			ORDER BY namespace ASC, key ASC`
		rows, err = d.QueryContext(ctx, query, scopeType, scopeID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query settings: %w", err)
	}
	defer rows.Close()

	var items []SettingItem
	for rows.Next() {
		var item SettingItem
		if err := rows.Scan(&item.ScopeType, &item.ScopeID, &item.Namespace, &item.Key, &item.ValueType, &item.Value, &item.UpdatedBy, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan setting item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// GetSettingHistory returns the latest audit log records for a setting key.
func (d *DB) GetSettingHistory(ctx context.Context, namespace, key string, limit int) ([]SettingChangeRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT id, scope_type, scope_id, namespace, key, old_val, new_val, changed_by, changed_at
		FROM setting_changes
		WHERE namespace = ? AND key = ?
		ORDER BY changed_at DESC, id DESC
		LIMIT ?`
	rows, err := d.QueryContext(ctx, query, namespace, key, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query setting history: %w", err)
	}
	defer rows.Close()

	var records []SettingChangeRecord
	for rows.Next() {
		var r SettingChangeRecord
		if err := rows.Scan(&r.ID, &r.ScopeType, &r.ScopeID, &r.Namespace, &r.Key, &r.OldVal, &r.NewVal, &r.ChangedBy, &r.ChangedAt); err != nil {
			return nil, fmt.Errorf("failed to scan setting change record: %w", err)
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

