package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
