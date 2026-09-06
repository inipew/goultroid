package database

import (
	"context"
	"fmt"
	"time"
)

// WarningRecord represents a single recorded warning infraction.
type WarningRecord struct {
	ID        int
	ChatID    int64
	UserID    int64
	Reason    string
	WarnedBy  int64
	CreatedAt time.Time
}

// AddWarning records an infraction against a user in a specific chat.
func (d *DB) AddWarning(ctx context.Context, chatID, userID int64, reason string, warnedBy int64) error {
	query := `
	INSERT INTO moderation_warnings (chat_id, user_id, reason, warned_by, created_at)
	VALUES (?, ?, ?, ?, ?)`

	_, err := d.ExecContext(ctx, query, chatID, userID, reason, warnedBy, time.Now())
	if err != nil {
		return fmt.Errorf("failed to insert warning: %w", err)
	}
	return nil
}

// GetWarnings retrieves all active infractions for a user in a specific chat, newest first.
func (d *DB) GetWarnings(ctx context.Context, chatID, userID int64) ([]*WarningRecord, error) {
	query := `
	SELECT id, chat_id, user_id, reason, warned_by, created_at
	FROM moderation_warnings
	WHERE chat_id = ? AND user_id = ?
	ORDER BY id DESC`

	rows, err := d.QueryContext(ctx, query, chatID, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query warnings: %w", err)
	}
	defer rows.Close()

	var records []*WarningRecord
	for rows.Next() {
		var r WarningRecord
		if err := rows.Scan(&r.ID, &r.ChatID, &r.UserID, &r.Reason, &r.WarnedBy, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan warning record: %w", err)
		}
		records = append(records, &r)
	}

	return records, rows.Err()
}

// GetWarningCount returns the total number of warnings for a user in a specific chat.
func (d *DB) GetWarningCount(ctx context.Context, chatID, userID int64) (int, error) {
	query := `SELECT COUNT(*) FROM moderation_warnings WHERE chat_id = ? AND user_id = ?`

	var count int
	err := d.QueryRowContext(ctx, query, chatID, userID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count warnings: %w", err)
	}
	return count, nil
}

// ResetWarnings removes all warnings for a user in a specific chat.
func (d *DB) ResetWarnings(ctx context.Context, chatID, userID int64) error {
	query := `DELETE FROM moderation_warnings WHERE chat_id = ? AND user_id = ?`

	_, err := d.ExecContext(ctx, query, chatID, userID)
	if err != nil {
		return fmt.Errorf("failed to delete warnings: %w", err)
	}
	return nil
}
