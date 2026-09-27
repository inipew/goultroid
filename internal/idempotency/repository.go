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
	EarliestExpiry(context.Context) (time.Time, bool, error)
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
			expires_at_ms INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'accepted',
			claim_token TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_idempotency_expires ON idempotency_keys(expires_at_ms);
	`)
	if err != nil {
		return fmt.Errorf("initialize idempotency schema: %w", err)
	}
	if err := r.ensureLifecycleColumns(ctx); err != nil {
		return err
	}
	return nil
}

func (r *SQLiteRepository) ensureLifecycleColumns(ctx context.Context) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{name: "status", ddl: "ALTER TABLE idempotency_keys ADD COLUMN status TEXT NOT NULL DEFAULT 'accepted'"},
		{name: "claim_token", ddl: "ALTER TABLE idempotency_keys ADD COLUMN claim_token TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		var count int
		if err := r.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM pragma_table_info('idempotency_keys') WHERE name = ?", column.name).Scan(&count); err != nil {
			return fmt.Errorf("inspect idempotency column %s: %w", column.name, err)
		}
		if count != 0 {
			continue
		}
		if _, err := r.db.ExecContext(ctx, column.ddl); err != nil {
			return fmt.Errorf("add idempotency column %s: %w", column.name, err)
		}
	}
	return nil
}

func (r *SQLiteRepository) Claim(ctx context.Context, key string, createdAt, expiresAt time.Time) (bool, error) {
	r.claimMu.Lock()
	defer r.claimMu.Unlock()
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO idempotency_keys(key, created_at_ms, expires_at_ms, status, claim_token)
		VALUES (?, ?, ?, 'accepted', '')
		ON CONFLICT(key) DO UPDATE SET
			created_at_ms = excluded.created_at_ms,
			expires_at_ms = excluded.expires_at_ms,
			status = 'accepted',
			claim_token = ''
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

func (r *SQLiteRepository) BeginClaim(ctx context.Context, key, token string, createdAt, expiresAt time.Time) (bool, error) {
	r.claimMu.Lock()
	defer r.claimMu.Unlock()
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO idempotency_keys(key, created_at_ms, expires_at_ms, status, claim_token)
		VALUES (?, ?, ?, 'processing', ?)
		ON CONFLICT(key) DO UPDATE SET
			created_at_ms = excluded.created_at_ms,
			expires_at_ms = excluded.expires_at_ms,
			status = 'processing',
			claim_token = excluded.claim_token
		WHERE idempotency_keys.expires_at_ms <= excluded.created_at_ms;
	`, key, createdAt.UnixMilli(), expiresAt.UnixMilli(), token)
	if err != nil {
		return false, fmt.Errorf("begin idempotency claim: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

func (r *SQLiteRepository) AcceptClaim(ctx context.Context, key, token string, expiresAt time.Time) (bool, error) {
	r.claimMu.Lock()
	defer r.claimMu.Unlock()
	result, err := r.db.ExecContext(ctx, `
		UPDATE idempotency_keys
		SET status = 'accepted', claim_token = '', expires_at_ms = ?
		WHERE key = ? AND status = 'processing' AND claim_token = ?;
	`, expiresAt.UnixMilli(), key, token)
	if err != nil {
		return false, fmt.Errorf("accept idempotency claim: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

func (r *SQLiteRepository) ReleaseClaim(ctx context.Context, key, token string) (bool, error) {
	r.claimMu.Lock()
	defer r.claimMu.Unlock()
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM idempotency_keys
		WHERE key = ? AND status = 'processing' AND claim_token = ?;
	`, key, token)
	if err != nil {
		return false, fmt.Errorf("release idempotency claim: %w", err)
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

func (r *SQLiteRepository) EarliestExpiry(ctx context.Context) (time.Time, bool, error) {
	var expiry sql.NullInt64
	if err := r.db.QueryRowContext(ctx, `SELECT MIN(expires_at_ms) FROM idempotency_keys`).Scan(&expiry); err != nil {
		return time.Time{}, false, err
	}
	if !expiry.Valid {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(expiry.Int64).UTC(), true, nil
}

func (r *SQLiteRepository) Size(ctx context.Context, now time.Time) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM idempotency_keys WHERE expires_at_ms > ?`, now.UnixMilli()).Scan(&count)
	return count, err
}
