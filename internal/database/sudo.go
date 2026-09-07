package database

import (
	"context"
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

// GetSudoUsers retrieves all registered sudo user IDs.
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

// AddSudoUser adds a user ID to the sudo list with the author ID.
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

// RemoveSudoUser deletes a user ID from the sudo list.
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

// IsSudoUser checks whether a user ID is registered in the sudo list.
func (d *DB) IsSudoUser(ctx context.Context, userID int64) (bool, error) {
	var exists bool
	err := d.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sudo_users WHERE user_id = ?)", userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check sudo user: %w", err)
	}
	return exists, nil
}
