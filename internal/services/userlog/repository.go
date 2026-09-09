package userlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

// Repository provides persistent access to user log configuration settings.
type Repository interface {
	InitSchema(ctx context.Context) error
	GetUserLogSetting(ctx context.Context, key string) (string, error)
	SetUserLogSetting(ctx context.Context, key, val string) error
}

// SQLiteRepository implements Repository backed by SQLExecutor.
type SQLiteRepository struct {
	db database.SQLExecutor
}

// NewSQLiteRepository creates a new SQLiteRepository.
func NewSQLiteRepository(db database.SQLExecutor) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

var _ Repository = (*SQLiteRepository)(nil)

// InitSchema ensures the user_log_settings table exists.
func (r *SQLiteRepository) InitSchema(ctx context.Context) error {
	query := `
	CREATE TABLE IF NOT EXISTS user_log_settings (
		key TEXT PRIMARY KEY,
		val TEXT NOT NULL
	);
	`
	_, err := r.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to init user_log_settings schema: %w", err)
	}
	return nil
}

// GetUserLogSetting retrieves a key-value setting for user logs.
func (r *SQLiteRepository) GetUserLogSetting(ctx context.Context, key string) (string, error) {
	var val string
	err := r.db.QueryRowContext(ctx, "SELECT val FROM user_log_settings WHERE key = ?", key).Scan(&val)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("failed to get user log setting: %w", err)
	}
	return val, nil
}

// SetUserLogSetting stores or updates a key-value setting for user logs.
func (r *SQLiteRepository) SetUserLogSetting(ctx context.Context, key, val string) error {
	query := `
		INSERT INTO user_log_settings (key, val)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET val = excluded.val;
	`
	_, err := r.db.ExecContext(ctx, query, key, val)
	if err != nil {
		return fmt.Errorf("failed to set user log setting: %w", err)
	}
	return nil
}
