package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AFK represents the AFK state of a user.
type AFK struct {
	UserID int64     `json:"user_id"`
	IsAFK  bool      `json:"is_afk"`
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}

// SetAFK sets or deactivates AFK status for a user.
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

// GetAFK retrieves the AFK status of a user.
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
