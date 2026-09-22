package deeplink

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

type SQLiteRepository struct {
	db *database.DB
}

func NewSQLiteRepository(db *database.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

func (r *SQLiteRepository) Create(ctx context.Context, token Token) error {
	if r == nil || r.db == nil {
		return ErrProviderUnavailable
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO assistant_deep_link_tokens (
			token, kind, payload, actor_id, single_use, consumed_at, expires_at, created_at
		) VALUES (?, ?, ?, ?, ?, NULL, ?, ?)
	`, token.ID, token.Kind, token.Payload, token.ActorID, token.SingleUse, token.ExpiresAt.UTC(), token.CreatedAt.UTC())
	if err != nil {
		if _, getErr := r.Get(ctx, token.ID); getErr == nil {
			return ErrTokenExists
		}
		return fmt.Errorf("create assistant deep-link token: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) Get(ctx context.Context, id string) (Token, error) {
	if r == nil || r.db == nil {
		return Token{}, ErrProviderUnavailable
	}
	var token Token
	var consumed sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT token, kind, payload, actor_id, single_use, consumed_at, expires_at, created_at
		FROM assistant_deep_link_tokens
		WHERE token = ?
	`, id).Scan(
		&token.ID, &token.Kind, &token.Payload, &token.ActorID, &token.SingleUse,
		&consumed, &token.ExpiresAt, &token.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrTokenNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("get assistant deep-link token: %w", err)
	}
	if consumed.Valid {
		at := consumed.Time.UTC()
		token.ConsumedAt = &at
	}
	token.ExpiresAt = token.ExpiresAt.UTC()
	token.CreatedAt = token.CreatedAt.UTC()
	return token, nil
}

func (r *SQLiteRepository) Claim(ctx context.Context, id string, actorID int64, now time.Time) (Token, error) {
	if r == nil || r.db == nil {
		return Token{}, ErrProviderUnavailable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Token{}, fmt.Errorf("begin deep-link claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var token Token
	var consumed sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT token, kind, payload, actor_id, single_use, consumed_at, expires_at, created_at
		FROM assistant_deep_link_tokens
		WHERE token = ?
	`, id).Scan(
		&token.ID, &token.Kind, &token.Payload, &token.ActorID, &token.SingleUse,
		&consumed, &token.ExpiresAt, &token.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrTokenNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("read deep-link claim: %w", err)
	}
	token.ExpiresAt = token.ExpiresAt.UTC()
	token.CreatedAt = token.CreatedAt.UTC()
	if !token.ExpiresAt.After(now.UTC()) {
		return Token{}, ErrTokenExpired
	}
	if consumed.Valid {
		return Token{}, ErrTokenConsumed
	}
	if token.ActorID != 0 && token.ActorID != actorID {
		return Token{}, ErrTokenUnauthorized
	}
	if token.SingleUse {
		result, err := tx.ExecContext(ctx, `
			UPDATE assistant_deep_link_tokens
			SET consumed_at = ?
			WHERE token = ? AND consumed_at IS NULL
		`, now.UTC(), id)
		if err != nil {
			return Token{}, fmt.Errorf("claim assistant deep-link token: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return Token{}, err
		}
		if affected != 1 {
			return Token{}, ErrTokenConsumed
		}
		at := now.UTC()
		token.ConsumedAt = &at
	}
	if err := tx.Commit(); err != nil {
		return Token{}, fmt.Errorf("commit assistant deep-link claim: %w", err)
	}
	return token, nil
}

func (r *SQLiteRepository) PruneExpired(ctx context.Context, now time.Time, limit int) (int, error) {
	if r == nil || r.db == nil {
		return 0, ErrProviderUnavailable
	}
	if limit <= 0 || limit > 256 {
		limit = 64
	}
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM assistant_deep_link_tokens
		WHERE token IN (
			SELECT token FROM assistant_deep_link_tokens
			WHERE expires_at <= ?
			ORDER BY expires_at ASC
			LIMIT ?
		)
	`, now.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("prune assistant deep-link tokens: %w", err)
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

var _ Repository = (*SQLiteRepository)(nil)
