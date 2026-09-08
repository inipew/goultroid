package admin

import (
	"context"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/moderation"
)

type sqliteWarningRepository struct {
	db *database.DB
}

func NewSQLiteWarningRepository(db *database.DB) moderation.WarningRepository {
	return &sqliteWarningRepository{db: db}
}

func (r *sqliteWarningRepository) AddWarning(ctx context.Context, chatID, userID int64, reason string, warnedBy int64) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("warning repository database is nil")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO moderation_warnings (chat_id, user_id, reason, warned_by, created_at)
		VALUES (?, ?, ?, ?, ?)`, chatID, userID, reason, warnedBy, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("failed to insert warning: %w", err)
	}
	return nil
}

func (r *sqliteWarningRepository) GetWarnings(ctx context.Context, chatID, userID int64) ([]*moderation.WarningRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("warning repository database is nil")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, chat_id, user_id, reason, warned_by, created_at
		FROM moderation_warnings
		WHERE chat_id = ? AND user_id = ?
		ORDER BY id DESC`, chatID, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query warnings: %w", err)
	}
	defer rows.Close()

	var records []*moderation.WarningRecord
	for rows.Next() {
		var record moderation.WarningRecord
		if err := rows.Scan(&record.ID, &record.ChatID, &record.UserID, &record.Reason, &record.WarnedBy, &record.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan warning record: %w", err)
		}
		records = append(records, &record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate warning records: %w", err)
	}
	return records, nil
}

func (r *sqliteWarningRepository) GetWarningCount(ctx context.Context, chatID, userID int64) (int, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("warning repository database is nil")
	}
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM moderation_warnings WHERE chat_id = ? AND user_id = ?`, chatID, userID).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count warnings: %w", err)
	}
	return count, nil
}

func (r *sqliteWarningRepository) ResetWarnings(ctx context.Context, chatID, userID int64) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("warning repository database is nil")
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM moderation_warnings WHERE chat_id = ? AND user_id = ?`, chatID, userID)
	if err != nil {
		return fmt.Errorf("failed to delete warnings: %w", err)
	}
	return nil
}
