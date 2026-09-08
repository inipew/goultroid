package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetUserLogSetting retrieves a key-value setting for user logs.
func (db *DB) GetUserLogSetting(ctx context.Context, key string) (string, error) {
	var val string
	err := db.QueryRowContext(ctx, "SELECT val FROM user_log_settings WHERE key = ?", key).Scan(&val)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("failed to get user log setting: %w", err)
	}
	return val, nil
}

// SetUserLogSetting stores or updates a key-value setting for user logs.
func (db *DB) SetUserLogSetting(ctx context.Context, key, val string) error {
	query := `
		INSERT INTO user_log_settings (key, val)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET val = excluded.val;
	`
	_, err := db.ExecContext(ctx, query, key, val)
	if err != nil {
		return fmt.Errorf("failed to set user log setting: %w", err)
	}
	return nil
}
