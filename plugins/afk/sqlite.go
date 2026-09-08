package afk

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

// SQLiteRepository implements Repository using *database.DB.
type SQLiteRepository struct {
	db *database.DB
}

// NewSQLiteRepository constructs a SQLiteRepository.
func NewSQLiteRepository(db *database.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

// SetAFK sets or deactivates AFK status for a user.
func (r *SQLiteRepository) SetAFK(ctx context.Context, userID int64, isAFK bool, reason string) error {
	now := time.Now().UTC()
	if isAFK {
		query := `
		INSERT INTO afk_status (user_id, is_afk, reason, since)
		VALUES (?, 1, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET is_afk = 1, reason = excluded.reason, since = excluded.since
		`
		_, err := r.db.ExecContext(ctx, query, userID, reason, now)
		if err != nil {
			return fmt.Errorf("failed to update afk status: %w", err)
		}
		return nil
	}

	// Atomic deactivate: update is_afk = 0 without overwriting the original since timestamp
	query := `UPDATE afk_status SET is_afk = 0 WHERE user_id = ? AND is_afk = 1`
	res, err := r.db.ExecContext(ctx, query, userID)
	if err != nil {
		return fmt.Errorf("failed to deactivate afk status: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		insertQuery := `
		INSERT INTO afk_status (user_id, is_afk, reason, since)
		VALUES (?, 0, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET is_afk = 0
		`
		_, _ = r.db.ExecContext(ctx, insertQuery, userID, reason, now)
	}
	return nil
}

// GetAFK retrieves the AFK status of a user.
func (r *SQLiteRepository) GetAFK(ctx context.Context, userID int64) (*AFK, error) {
	query := "SELECT user_id, is_afk, reason, since FROM afk_status WHERE user_id = ?"
	row := r.db.QueryRowContext(ctx, query, userID)

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

var _ Repository = (*SQLiteRepository)(nil)
