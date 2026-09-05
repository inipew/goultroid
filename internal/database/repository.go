package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

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

// ScheduledJob represents a scheduled task (one-shot or recurring).
type ScheduledJob struct {
	ID              int64     `json:"id"`
	ChatID          int64     `json:"chat_id"`
	PeerType        string    `json:"peer_type"`   // "user", "chat", "channel", "self"
	AccessHash      int64     `json:"access_hash"` // access hash for user/channel peer resolution
	ActionType      string    `json:"action_type"` // "message" or "command"
	Payload         string    `json:"payload"`     // message text or ".command ..."
	IntervalSeconds int64     `json:"interval_seconds"`
	NextRunAt       time.Time `json:"next_run_at"`
	CreatedAt       time.Time `json:"created_at"`
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
	UpdateScheduledJobNextRun(ctx context.Context, id int64, nextRun time.Time) error
	DeleteScheduledJob(ctx context.Context, id int64) error
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
	query := `
	INSERT INTO afk_status (user_id, is_afk, reason, since)
	VALUES (?, ?, ?, ?)
	ON CONFLICT(user_id) DO UPDATE SET is_afk = excluded.is_afk, reason = excluded.reason, since = excluded.since
	`
	_, err := d.ExecContext(ctx, query, userID, isAFK, reason, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("failed to update afk status: %w", err)
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

func (d *DB) CreateScheduledJob(ctx context.Context, job *ScheduledJob) (*ScheduledJob, error) {
	if job == nil {
		return nil, errors.New("job cannot be nil")
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}

	query := `INSERT INTO scheduled_jobs (chat_id, peer_type, access_hash, action_type, payload, interval_seconds, next_run_at, created_at)
	          VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	res, err := d.ExecContext(ctx, query, job.ChatID, job.PeerType, job.AccessHash, job.ActionType, job.Payload, job.IntervalSeconds, job.NextRunAt, job.CreatedAt)
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
	query := `SELECT id, chat_id, peer_type, access_hash, action_type, payload, interval_seconds, next_run_at, created_at
	          FROM scheduled_jobs WHERE id = ?`
	row := d.QueryRowContext(ctx, query, id)

	var job ScheduledJob
	if err := row.Scan(&job.ID, &job.ChatID, &job.PeerType, &job.AccessHash, &job.ActionType, &job.Payload, &job.IntervalSeconds, &job.NextRunAt, &job.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get scheduled job: %w", err)
	}
	return &job, nil
}

func (d *DB) ListScheduledJobs(ctx context.Context, chatID int64) ([]ScheduledJob, error) {
	query := `SELECT id, chat_id, peer_type, access_hash, action_type, payload, interval_seconds, next_run_at, created_at
	          FROM scheduled_jobs WHERE chat_id = ? ORDER BY next_run_at ASC`
	rows, err := d.QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list scheduled jobs: %w", err)
	}
	defer rows.Close()

	var jobs []ScheduledJob
	for rows.Next() {
		var job ScheduledJob
		if err := rows.Scan(&job.ID, &job.ChatID, &job.PeerType, &job.AccessHash, &job.ActionType, &job.Payload, &job.IntervalSeconds, &job.NextRunAt, &job.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan scheduled job: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (d *DB) ListDueScheduledJobs(ctx context.Context, before time.Time) ([]ScheduledJob, error) {
	query := `SELECT id, chat_id, peer_type, access_hash, action_type, payload, interval_seconds, next_run_at, created_at
	          FROM scheduled_jobs WHERE next_run_at <= ? ORDER BY next_run_at ASC`
	rows, err := d.QueryContext(ctx, query, before)
	if err != nil {
		return nil, fmt.Errorf("failed to list due scheduled jobs: %w", err)
	}
	defer rows.Close()

	var jobs []ScheduledJob
	for rows.Next() {
		var job ScheduledJob
		if err := rows.Scan(&job.ID, &job.ChatID, &job.PeerType, &job.AccessHash, &job.ActionType, &job.Payload, &job.IntervalSeconds, &job.NextRunAt, &job.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan due scheduled job: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
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


