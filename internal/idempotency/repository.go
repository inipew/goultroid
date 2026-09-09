package idempotency

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// Repository atomically stores idempotency claims across process restarts.
type Repository interface {
	InitSchema(context.Context) error
	Claim(context.Context, string, time.Time, time.Time) (bool, error)
	IsProcessed(context.Context, string, time.Time) (bool, error)
	DeleteExpired(context.Context, time.Time) (int, error)
	Size(context.Context, time.Time) (int, error)
}

type SQLiteRepository struct {
	db      *sql.DB
	claimMu sync.Mutex
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository { return &SQLiteRepository{db: db} }

func (r *SQLiteRepository) InitSchema(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS idempotency_keys (
			key TEXT PRIMARY KEY,
			created_at_ms INTEGER NOT NULL,
			expires_at_ms INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_idempotency_expires ON idempotency_keys(expires_at_ms);
	`)
	if err != nil {
		return fmt.Errorf("initialize idempotency schema: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) Claim(ctx context.Context, key string, createdAt, expiresAt time.Time) (bool, error) {
	r.claimMu.Lock()
	defer r.claimMu.Unlock()
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO idempotency_keys(key, created_at_ms, expires_at_ms)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			created_at_ms = excluded.created_at_ms,
			expires_at_ms = excluded.expires_at_ms
		WHERE idempotency_keys.expires_at_ms <= excluded.created_at_ms;
	`, key, createdAt.UnixMilli(), expiresAt.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("claim idempotency key: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

func (r *SQLiteRepository) IsProcessed(ctx context.Context, key string, now time.Time) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM idempotency_keys WHERE key = ? AND expires_at_ms > ?`, key, now.UnixMilli()).Scan(&count)
	return count > 0, err
}

func (r *SQLiteRepository) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE expires_at_ms <= ?`, now.UnixMilli())
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	return int(rows), err
}

func (r *SQLiteRepository) Size(ctx context.Context, now time.Time) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM idempotency_keys WHERE expires_at_ms > ?`, now.UnixMilli()).Scan(&count)
	return count, err
}
