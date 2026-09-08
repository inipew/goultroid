package database

import (
	"context"
	"time"
)

// WarningRecord is a persisted moderation warning.
type WarningRecord struct {
	ID        int
	ChatID    int64
	UserID    int64
	Reason    string
	WarnedBy  int64
	CreatedAt time.Time
}

// GetWarningCount returns the number of active warnings for a user in a chat.
func (d *DB) GetWarningCount(ctx context.Context, chatID, userID int64) (int, error) {
	var count int
	err := d.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM moderation_warnings
		WHERE chat_id = ? AND user_id = ?
	`, chatID, userID).Scan(&count)
	return count, err
}

// AddWarning persists a new moderation warning.
func (d *DB) AddWarning(ctx context.Context, chatID, userID int64, reason string, warnedBy int64) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO moderation_warnings (chat_id, user_id, reason, warned_by, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, chatID, userID, reason, warnedBy, time.Now().UTC())
	return err
}

// GetWarnings returns active warnings newest first.
func (d *DB) GetWarnings(ctx context.Context, chatID, userID int64) ([]*WarningRecord, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, chat_id, user_id, reason, warned_by, created_at
		FROM moderation_warnings
		WHERE chat_id = ? AND user_id = ?
		ORDER BY id DESC
	`, chatID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*WarningRecord
	for rows.Next() {
		record := new(WarningRecord)
		if err := rows.Scan(
			&record.ID,
			&record.ChatID,
			&record.UserID,
			&record.Reason,
			&record.WarnedBy,
			&record.CreatedAt,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// ResetWarnings removes all active warnings for a user in a chat.
func (d *DB) ResetWarnings(ctx context.Context, chatID, userID int64) error {
	_, err := d.ExecContext(ctx, `
		DELETE FROM moderation_warnings
		WHERE chat_id = ? AND user_id = ?
	`, chatID, userID)
	return err
}
