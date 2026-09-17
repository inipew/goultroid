package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/deeplink"
)

type Repository struct {
	db *sql.DB
}

var _ deeplink.Repository = (*Repository)(nil)

// NewRepository creates a new SQLite-backed deep link repository.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, rec deeplink.Record) error {
	singleUseInt := 0
	if rec.SingleUse {
		singleUseInt = 1
	}

	var consumedAtInt sql.NullInt64
	if rec.ConsumedAt != nil {
		consumedAtInt = sql.NullInt64{Int64: rec.ConsumedAt.Unix(), Valid: true}
	}

	payload := rec.Payload
	if payload == nil {
		payload = []byte{}
	}

	query := `INSERT INTO assistant_deep_links (
		id, token_hash, purpose, owner, generation, user_id, source_chat_id,
		screen_namespace, screen_name, screen_version, payload_type, payload_version,
		payload, single_use, issued_at, expires_at, consumed_at, consumed_by
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := r.db.ExecContext(ctx, query,
		rec.ID,
		rec.TokenHash,
		string(rec.Purpose),
		rec.Owner,
		rec.Generation,
		rec.UserID,
		rec.SourceChatID,
		rec.Screen.Namespace,
		rec.Screen.Name,
		rec.Screen.Version,
		rec.PayloadType,
		rec.PayloadVersion,
		payload,
		singleUseInt,
		rec.IssuedAt.Unix(),
		rec.ExpiresAt.Unix(),
		consumedAtInt,
		rec.ConsumedBy,
	)
	if err != nil {
		return fmt.Errorf("insert deep link: %w", err)
	}
	return nil
}

func (r *Repository) FindByHash(ctx context.Context, tokenHash []byte) (deeplink.Record, error) {
	query := `SELECT id, token_hash, purpose, owner, generation, user_id, source_chat_id,
		screen_namespace, screen_name, screen_version, payload_type, payload_version,
		payload, single_use, issued_at, expires_at, consumed_at, consumed_by
	FROM assistant_deep_links WHERE token_hash = ?`

	row := r.db.QueryRowContext(ctx, query, tokenHash)
	return scanRecord(row)
}

func (r *Repository) ConsumeAtomic(ctx context.Context, tokenHash []byte, consumedBy int64, now time.Time, validate func(rec deeplink.Record) error) (deeplink.Record, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deeplink.Record{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := `SELECT id, token_hash, purpose, owner, generation, user_id, source_chat_id,
		screen_namespace, screen_name, screen_version, payload_type, payload_version,
		payload, single_use, issued_at, expires_at, consumed_at, consumed_by
	FROM assistant_deep_links WHERE token_hash = ?`

	row := tx.QueryRowContext(ctx, query, tokenHash)
	rec, err := scanRecord(row)
	if err != nil {
		return deeplink.Record{}, err
	}

	if now.After(rec.ExpiresAt) {
		return deeplink.Record{}, deeplink.ErrTokenExpired
	}

	if rec.SingleUse && rec.ConsumedAt != nil {
		return deeplink.Record{}, deeplink.ErrTokenConsumed
	}

	if validate != nil {
		if err := validate(rec); err != nil {
			return deeplink.Record{}, err
		}
	}

	nowUnix := now.Unix()
	if rec.SingleUse {
		updateQuery := `UPDATE assistant_deep_links
		SET consumed_at = ?, consumed_by = ?
		WHERE token_hash = ? AND (consumed_at IS NULL OR consumed_at = 0)`

		res, err := tx.ExecContext(ctx, updateQuery, nowUnix, consumedBy, tokenHash)
		if err != nil {
			return deeplink.Record{}, fmt.Errorf("update consumed: %w", err)
		}
		rowsAff, err := res.RowsAffected()
		if err != nil {
			return deeplink.Record{}, err
		}
		if rowsAff == 0 {
			return deeplink.Record{}, deeplink.ErrTokenConsumed
		}
	}

	if err := tx.Commit(); err != nil {
		return deeplink.Record{}, fmt.Errorf("commit tx: %w", err)
	}

	consumedAtTime := time.Unix(nowUnix, 0)
	rec.ConsumedAt = &consumedAtTime
	rec.ConsumedBy = consumedBy
	return rec, nil
}

func (r *Repository) RevokeOwner(ctx context.Context, owner string, generation uint64) (int64, error) {
	var res sql.Result
	var err error
	if generation == 0 {
		query := `DELETE FROM assistant_deep_links WHERE owner = ?`
		res, err = r.db.ExecContext(ctx, query, owner)
	} else {
		query := `DELETE FROM assistant_deep_links WHERE owner = ? AND generation = ?`
		res, err = r.db.ExecContext(ctx, query, owner, generation)
	}
	if err != nil {
		return 0, fmt.Errorf("revoke owner links: %w", err)
	}
	return res.RowsAffected()
}

func (r *Repository) Prune(ctx context.Context, olderThan time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 100
	}
	olderUnix := olderThan.Unix()
	query := `DELETE FROM assistant_deep_links WHERE id IN (
		SELECT id FROM assistant_deep_links
		WHERE expires_at < ? OR (consumed_at IS NOT NULL AND consumed_at > 0 AND consumed_at < ?)
		LIMIT ?
	)`
	res, err := r.db.ExecContext(ctx, query, olderUnix, olderUnix, limit)
	if err != nil {
		return 0, fmt.Errorf("prune deep links: %w", err)
	}
	return res.RowsAffected()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(row rowScanner) (deeplink.Record, error) {
	var (
		rec             deeplink.Record
		purposeStr      string
		singleUseInt    int
		issuedAtUnix    int64
		expiresAtUnix   int64
		consumedAtNull  sql.NullInt64
		consumedByNull  sql.NullInt64
		screenNamespace string
		screenName      string
		screenVersion   int
	)

	err := row.Scan(
		&rec.ID,
		&rec.TokenHash,
		&purposeStr,
		&rec.Owner,
		&rec.Generation,
		&rec.UserID,
		&rec.SourceChatID,
		&screenNamespace,
		&screenName,
		&screenVersion,
		&rec.PayloadType,
		&rec.PayloadVersion,
		&rec.Payload,
		&singleUseInt,
		&issuedAtUnix,
		&expiresAtUnix,
		&consumedAtNull,
		&consumedByNull,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return deeplink.Record{}, deeplink.ErrTokenNotFound
		}
		return deeplink.Record{}, fmt.Errorf("scan deep link record: %w", err)
	}

	rec.Purpose = deeplink.Purpose(purposeStr)
	rec.SingleUse = singleUseInt == 1
	rec.IssuedAt = time.Unix(issuedAtUnix, 0)
	rec.ExpiresAt = time.Unix(expiresAtUnix, 0)
	if consumedAtNull.Valid && consumedAtNull.Int64 > 0 {
		t := time.Unix(consumedAtNull.Int64, 0)
		rec.ConsumedAt = &t
	}
	if consumedByNull.Valid {
		rec.ConsumedBy = consumedByNull.Int64
	}
	rec.Screen = presentation.ScreenKey{
		Namespace: screenNamespace,
		Name:      screenName,
		Version:   uint16(screenVersion),
	}

	return rec, nil
}
