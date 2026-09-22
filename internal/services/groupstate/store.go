package groupstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

const (
	DefaultMaxEntries   = 50_000
	HardMaxEntries      = 200_000
	DefaultCleanupBatch = 64
	HardCleanupBatch    = 512
)

var (
	ErrStateNotFound = fmt.Errorf("%w: group state not found", core.ErrNotFound)
	ErrStateConflict = fmt.Errorf("%w: group state revision conflict", core.ErrConflict)
	ErrStateCapacity = fmt.Errorf("%w: group state capacity reached", core.ErrResourceLimit)
	ErrInvalidState  = fmt.Errorf("%w: invalid group state", core.ErrInvalidArgs)
)

type Limits struct {
	MaxEntries   int
	CleanupBatch int
}

func DefaultLimits() Limits {
	return Limits{MaxEntries: DefaultMaxEntries, CleanupBatch: DefaultCleanupBatch}
}

func (l Limits) normalize() (Limits, error) {
	if l.MaxEntries == 0 {
		l.MaxEntries = DefaultMaxEntries
	}
	if l.CleanupBatch == 0 {
		l.CleanupBatch = DefaultCleanupBatch
	}
	if l.MaxEntries < 1 || l.MaxEntries > HardMaxEntries {
		return Limits{}, fmt.Errorf("%w: max entries must be between 1 and %d", ErrInvalidState, HardMaxEntries)
	}
	if l.CleanupBatch < 1 || l.CleanupBatch > HardCleanupBatch {
		return Limits{}, fmt.Errorf("%w: cleanup batch must be between 1 and %d", ErrInvalidState, HardCleanupBatch)
	}
	return l, nil
}

// SQLiteStore is a bounded durable implementation of core.GroupStateStore.
// It owns no goroutine/cache/ticker; cleanup is occurrence-driven and bounded.
type SQLiteStore struct {
	db       *database.DB
	limits   Limits
	now      func() time.Time
	createMu sync.Mutex
}

var _ core.GroupStateStore = (*SQLiteStore)(nil)

func NewSQLiteStore(db *database.DB) *SQLiteStore {
	store, err := NewSQLiteStoreWithLimits(db, DefaultLimits())
	if err != nil {
		return nil
	}
	return store
}

func NewSQLiteStoreWithLimits(db *database.DB, limits Limits) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: database is nil", ErrInvalidState)
	}
	normalized, err := limits.normalize()
	if err != nil {
		return nil, err
	}
	return &SQLiteStore{db: db, limits: normalized, now: time.Now}, nil
}

func normalizeKey(key core.GroupStateKey) (core.GroupStateKey, error) {
	key.Namespace = strings.ToLower(strings.TrimSpace(key.Namespace))
	key.Key = strings.ToLower(strings.TrimSpace(key.Key))
	if key.ChatID <= 0 {
		return core.GroupStateKey{}, fmt.Errorf("%w: chat id must be positive", ErrInvalidState)
	}
	if key.Namespace == "" || len(key.Namespace) > core.MaxGroupStateNamespaceBytes {
		return core.GroupStateKey{}, fmt.Errorf("%w: invalid namespace", ErrInvalidState)
	}
	if key.Key == "" || len(key.Key) > core.MaxGroupStateKeyBytes {
		return core.GroupStateKey{}, fmt.Errorf("%w: invalid key", ErrInvalidState)
	}
	return key, nil
}

func normalizeCAS(req core.GroupStateCAS, now time.Time) (core.GroupStateCAS, error) {
	key, err := normalizeKey(req.GroupStateKey)
	if err != nil {
		return core.GroupStateCAS{}, err
	}
	req.GroupStateKey = key
	if req.ExpectedRevision >= uint64(math.MaxInt64) {
		return core.GroupStateCAS{}, fmt.Errorf("%w: expected revision is too large", ErrInvalidState)
	}
	if len(req.Value) > core.MaxGroupStateValueBytes {
		return core.GroupStateCAS{}, fmt.Errorf("%w: value exceeds %d bytes", ErrInvalidState, core.MaxGroupStateValueBytes)
	}
	if req.UpdatedBy <= 0 {
		return core.GroupStateCAS{}, fmt.Errorf("%w: updated_by must be positive", ErrInvalidState)
	}
	if req.UpdatedAt.IsZero() {
		req.UpdatedAt = now
	} else {
		req.UpdatedAt = req.UpdatedAt.UTC()
	}
	if req.ExpiresAt != nil {
		expiry := req.ExpiresAt.UTC()
		if !expiry.After(req.UpdatedAt) {
			return core.GroupStateCAS{}, fmt.Errorf("%w: expires_at must be after updated_at", ErrInvalidState)
		}
		req.ExpiresAt = &expiry
	}
	req.Value = append([]byte(nil), req.Value...)
	return req, nil
}

func scanRecord(scanner interface{ Scan(...any) error }) (core.GroupStateRecord, error) {
	var (
		record    core.GroupStateRecord
		revision  int64
		expiresAt sql.NullTime
	)
	err := scanner.Scan(
		&record.ChatID,
		&record.Namespace,
		&record.Key,
		&record.Value,
		&revision,
		&record.UpdatedBy,
		&record.UpdatedAt,
		&expiresAt,
	)
	if err != nil {
		return core.GroupStateRecord{}, err
	}
	if revision <= 0 {
		return core.GroupStateRecord{}, fmt.Errorf("%w: persisted revision %d is invalid", ErrInvalidState, revision)
	}
	record.Revision = uint64(revision)
	record.Value = append([]byte(nil), record.Value...)
	record.UpdatedAt = record.UpdatedAt.UTC()
	if expiresAt.Valid {
		expiry := expiresAt.Time.UTC()
		record.ExpiresAt = &expiry
	}
	return record, nil
}

func (s *SQLiteStore) Get(ctx context.Context, key core.GroupStateKey) (core.GroupStateRecord, error) {
	if s == nil || s.db == nil {
		return core.GroupStateRecord{}, fmt.Errorf("%w: store unavailable", core.ErrUnavailable)
	}
	key, err := normalizeKey(key)
	if err != nil {
		return core.GroupStateRecord{}, err
	}
	now := s.now().UTC()
	record, err := scanRecord(s.db.QueryRowContext(ctx, `
		SELECT chat_id, namespace, key, value, revision, updated_by, updated_at, expires_at
		FROM assistant_group_state
		WHERE chat_id = ? AND namespace = ? AND key = ?
		  AND (expires_at IS NULL OR expires_at > ?)
	`, key.ChatID, key.Namespace, key.Key, now))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.GroupStateRecord{}, ErrStateNotFound
		}
		return core.GroupStateRecord{}, fmt.Errorf("get group state: %w", err)
	}
	return record, nil
}

func deleteExpiredTx(ctx context.Context, tx *sql.Tx, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	result, err := tx.ExecContext(ctx, `
		DELETE FROM assistant_group_state
		WHERE rowid IN (
			SELECT rowid FROM assistant_group_state
			WHERE expires_at IS NOT NULL AND expires_at <= ?
			ORDER BY expires_at ASC, chat_id ASC, namespace ASC, key ASC
			LIMIT ?
		)
	`, now, limit)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func countTx(ctx context.Context, tx *sql.Tx) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM assistant_group_state`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func liveRevisionTx(ctx context.Context, tx *sql.Tx, key core.GroupStateKey, now time.Time) (uint64, error) {
	var revision int64
	err := tx.QueryRowContext(ctx, `
		SELECT revision FROM assistant_group_state
		WHERE chat_id = ? AND namespace = ? AND key = ?
		  AND (expires_at IS NULL OR expires_at > ?)
	`, key.ChatID, key.Namespace, key.Key, now).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrStateNotFound
	}
	if err != nil {
		return 0, err
	}
	if revision <= 0 {
		return 0, fmt.Errorf("%w: persisted revision %d is invalid", ErrInvalidState, revision)
	}
	return uint64(revision), nil
}

func readTx(ctx context.Context, tx *sql.Tx, key core.GroupStateKey, now time.Time) (core.GroupStateRecord, error) {
	record, err := scanRecord(tx.QueryRowContext(ctx, `
		SELECT chat_id, namespace, key, value, revision, updated_by, updated_at, expires_at
		FROM assistant_group_state
		WHERE chat_id = ? AND namespace = ? AND key = ?
		  AND (expires_at IS NULL OR expires_at > ?)
	`, key.ChatID, key.Namespace, key.Key, now))
	if errors.Is(err, sql.ErrNoRows) {
		return core.GroupStateRecord{}, ErrStateNotFound
	}
	return record, err
}

func (s *SQLiteStore) CompareAndSwap(ctx context.Context, grant core.GroupStateWriteGrant, req core.GroupStateCAS) (core.GroupStateRecord, error) {
	if s == nil || s.db == nil {
		return core.GroupStateRecord{}, fmt.Errorf("%w: store unavailable", core.ErrUnavailable)
	}
	now := s.now().UTC()
	req, err := normalizeCAS(req, now)
	if err != nil {
		return core.GroupStateRecord{}, err
	}
	if !grant.Authorizes(req.ChatID, req.UpdatedBy) {
		return core.GroupStateRecord{}, fmt.Errorf("%w: invalid group-state write grant", core.ErrGroupAuthorizationDenied)
	}

	if req.ExpectedRevision == 0 {
		s.createMu.Lock()
		defer s.createMu.Unlock()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.GroupStateRecord{}, fmt.Errorf("begin group state CAS: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Exact-key expiry is reclaimed first so create-only can reuse its durable
	// coordinate without turning an expired record into a revision successor.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM assistant_group_state
		WHERE chat_id = ? AND namespace = ? AND key = ?
		  AND expires_at IS NOT NULL AND expires_at <= ?
	`, req.ChatID, req.Namespace, req.Key, now); err != nil {
		return core.GroupStateRecord{}, fmt.Errorf("reclaim expired group state coordinate: %w", err)
	}

	if req.ExpectedRevision == 0 {
		if _, err := liveRevisionTx(ctx, tx, req.GroupStateKey, now); err == nil {
			return core.GroupStateRecord{}, ErrStateConflict
		} else if !errors.Is(err, ErrStateNotFound) {
			return core.GroupStateRecord{}, fmt.Errorf("check group state create conflict: %w", err)
		}

		count, err := countTx(ctx, tx)
		if err != nil {
			return core.GroupStateRecord{}, fmt.Errorf("count group state capacity: %w", err)
		}
		if count >= s.limits.MaxEntries {
			if _, err := deleteExpiredTx(ctx, tx, now, s.limits.CleanupBatch); err != nil {
				return core.GroupStateRecord{}, fmt.Errorf("prune group state at capacity: %w", err)
			}
			count, err = countTx(ctx, tx)
			if err != nil {
				return core.GroupStateRecord{}, fmt.Errorf("recount group state capacity: %w", err)
			}
			if count >= s.limits.MaxEntries {
				return core.GroupStateRecord{}, ErrStateCapacity
			}
		}

		result, err := tx.ExecContext(ctx, `
			INSERT INTO assistant_group_state (
				chat_id, namespace, key, value, revision, updated_by, updated_at, expires_at
			) VALUES (?, ?, ?, ?, 1, ?, ?, ?)
			ON CONFLICT(chat_id, namespace, key) DO NOTHING
		`, req.ChatID, req.Namespace, req.Key, req.Value, req.UpdatedBy, req.UpdatedAt, req.ExpiresAt)
		if err != nil {
			return core.GroupStateRecord{}, fmt.Errorf("insert group state: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return core.GroupStateRecord{}, fmt.Errorf("read group state create result: %w", err)
		}
		if affected != 1 {
			return core.GroupStateRecord{}, ErrStateConflict
		}
	} else {
		result, err := tx.ExecContext(ctx, `
			UPDATE assistant_group_state
			SET value = ?, revision = revision + 1, updated_by = ?, updated_at = ?, expires_at = ?
			WHERE chat_id = ? AND namespace = ? AND key = ?
			  AND revision = ?
			  AND (expires_at IS NULL OR expires_at > ?)
		`,
			req.Value, req.UpdatedBy, req.UpdatedAt, req.ExpiresAt,
			req.ChatID, req.Namespace, req.Key, req.ExpectedRevision, now,
		)
		if err != nil {
			return core.GroupStateRecord{}, fmt.Errorf("update group state CAS: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return core.GroupStateRecord{}, fmt.Errorf("read group state CAS result: %w", err)
		}
		if affected != 1 {
			current, lookupErr := liveRevisionTx(ctx, tx, req.GroupStateKey, now)
			switch {
			case errors.Is(lookupErr, ErrStateNotFound):
				return core.GroupStateRecord{}, ErrStateNotFound
			case lookupErr != nil:
				return core.GroupStateRecord{}, fmt.Errorf("inspect group state CAS conflict: %w", lookupErr)
			case current != req.ExpectedRevision:
				return core.GroupStateRecord{}, ErrStateConflict
			default:
				return core.GroupStateRecord{}, ErrStateConflict
			}
		}
	}

	record, err := readTx(ctx, tx, req.GroupStateKey, now)
	if err != nil {
		return core.GroupStateRecord{}, fmt.Errorf("read committed group state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return core.GroupStateRecord{}, fmt.Errorf("commit group state CAS: %w", err)
	}
	return record, nil
}

func (s *SQLiteStore) DeleteCompareAndSwap(ctx context.Context, grant core.GroupStateWriteGrant, req core.GroupStateDelete) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("%w: store unavailable", core.ErrUnavailable)
	}
	key, err := normalizeKey(req.GroupStateKey)
	if err != nil {
		return err
	}
	req.GroupStateKey = key
	if req.ExpectedRevision == 0 || req.ExpectedRevision > uint64(math.MaxInt64) || req.DeletedBy <= 0 {
		return fmt.Errorf("%w: delete requires a valid revision and actor", ErrInvalidState)
	}
	if !grant.Authorizes(req.ChatID, req.DeletedBy) {
		return fmt.Errorf("%w: invalid group-state write grant", core.ErrGroupAuthorizationDenied)
	}
	now := s.now().UTC()
	if req.DeletedAt.IsZero() {
		req.DeletedAt = now
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin group state delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		DELETE FROM assistant_group_state
		WHERE chat_id = ? AND namespace = ? AND key = ?
		  AND revision = ?
		  AND (expires_at IS NULL OR expires_at > ?)
	`, req.ChatID, req.Namespace, req.Key, req.ExpectedRevision, now)
	if err != nil {
		return fmt.Errorf("delete group state CAS: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read group state delete result: %w", err)
	}
	if affected != 1 {
		current, lookupErr := liveRevisionTx(ctx, tx, req.GroupStateKey, now)
		switch {
		case errors.Is(lookupErr, ErrStateNotFound):
			return ErrStateNotFound
		case lookupErr != nil:
			return fmt.Errorf("inspect group state delete conflict: %w", lookupErr)
		case current != req.ExpectedRevision:
			return ErrStateConflict
		default:
			return ErrStateConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit group state delete: %w", err)
	}
	return nil
}

func (s *SQLiteStore) PruneExpired(ctx context.Context, now time.Time, limit int) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("%w: store unavailable", core.ErrUnavailable)
	}
	if now.IsZero() {
		now = s.now().UTC()
	} else {
		now = now.UTC()
	}
	if limit <= 0 {
		limit = s.limits.CleanupBatch
	}
	if limit > HardCleanupBatch {
		return 0, fmt.Errorf("%w: cleanup limit exceeds %d", ErrInvalidState, HardCleanupBatch)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin group state prune: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	pruned, err := deleteExpiredTx(ctx, tx, now, limit)
	if err != nil {
		return 0, fmt.Errorf("prune expired group state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit group state prune: %w", err)
	}
	return pruned, nil
}

func (s *SQLiteStore) Count(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("%w: store unavailable", core.ErrUnavailable)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assistant_group_state`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count group state: %w", err)
	}
	return count, nil
}
