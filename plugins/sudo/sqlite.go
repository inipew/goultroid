package sudo

import (
	"context"
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

// GetSudoUsers retrieves all registered sudo user IDs.
func (r *SQLiteRepository) GetSudoUsers(ctx context.Context) ([]User, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT user_id, added_at, added_by FROM sudo_users ORDER BY user_id ASC")
	if err != nil {
		return nil, fmt.Errorf("failed to query sudo users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
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

// AddSudoUser adds a user ID to the sudo list with the author ID.
func (r *SQLiteRepository) AddSudoUser(ctx context.Context, userID, addedBy int64) error {
	query := `
	INSERT INTO sudo_users (user_id, added_at, added_by)
	VALUES (?, ?, ?)
	ON CONFLICT(user_id) DO UPDATE SET added_at = excluded.added_at, added_by = excluded.added_by
	`
	_, err := r.db.ExecContext(ctx, query, userID, time.Now().UTC(), addedBy)
	if err != nil {
		return fmt.Errorf("failed to add sudo user: %w", err)
	}
	return nil
}

// RemoveSudoUser deletes a user ID from the sudo list.
func (r *SQLiteRepository) RemoveSudoUser(ctx context.Context, userID int64) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM sudo_users WHERE user_id = ?", userID)
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

// IsSudoUser checks whether a user ID is registered in the sudo list.
func (r *SQLiteRepository) IsSudoUser(ctx context.Context, userID int64) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sudo_users WHERE user_id = ?)", userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check sudo user: %w", err)
	}
	return exists, nil
}

var _ Repository = (*SQLiteRepository)(nil)
