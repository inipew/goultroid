package savedresponse

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

const (
	defaultCleanupBatch  = 16
	maxCleanupBatch      = 128
	maxCleanupErrorLen   = 1024
	preparedCleanupGrace = time.Minute
)

type cleanupItem struct {
	AssetID  string
	Attempts int
}

type cleanupJournal struct {
	db *database.DB
}

type CleanupStats struct {
	Scanned    int
	Deleted    int
	Referenced int
	Deferred   int
}

func newCleanupJournal(db *database.DB) *cleanupJournal {
	if db == nil {
		return nil
	}
	return &cleanupJournal{db: db}
}

func (j *cleanupJournal) enqueue(ctx context.Context, assetID string) error {
	return j.enqueueAt(ctx, assetID, time.Now().UTC())
}

func (j *cleanupJournal) prepare(ctx context.Context, assetID string) error {
	return j.enqueueAt(ctx, assetID, time.Now().UTC().Add(preparedCleanupGrace))
}

func (j *cleanupJournal) enqueueIfAbsent(ctx context.Context, assetID string) (bool, error) {
	if j == nil || j.db == nil {
		return false, nil
	}
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	res, err := j.db.ExecContext(ctx, `
		INSERT INTO saved_response_media_cleanup (
			asset_id, attempts, last_error, next_attempt_at, created_at, updated_at
		) VALUES (?, 0, '', ?, ?, ?)
		ON CONFLICT(asset_id) DO NOTHING
	`, assetID, now, now, now)
	if err != nil {
		return false, fmt.Errorf("saved response: enqueue discovered media cleanup %q: %w", assetID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

func (j *cleanupJournal) enqueueAt(ctx context.Context, assetID string, nextAttempt time.Time) error {
	if j == nil || j.db == nil {
		return nil
	}
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	_, err := j.db.ExecContext(ctx, `
		INSERT INTO saved_response_media_cleanup (
			asset_id, attempts, last_error, next_attempt_at, created_at, updated_at
		) VALUES (?, 0, '', ?, ?, ?)
		ON CONFLICT(asset_id) DO UPDATE SET
			attempts = 0,
			last_error = '',
			next_attempt_at = excluded.next_attempt_at,
			updated_at = excluded.updated_at
	`, assetID, nextAttempt.UTC(), now, now)
	if err != nil {
		return fmt.Errorf("saved response: enqueue media cleanup %q: %w", assetID, err)
	}
	return nil
}

func (j *cleanupJournal) activate(ctx context.Context, assetID string) error {
	if j == nil || j.db == nil || strings.TrimSpace(assetID) == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	res, err := j.db.ExecContext(ctx, `
		UPDATE saved_response_media_cleanup
		SET attempts = 0, last_error = '', next_attempt_at = ?, updated_at = ?
		WHERE asset_id = ?
	`, now, now, assetID)
	if err != nil {
		return fmt.Errorf("saved response: activate media cleanup %q: %w", assetID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("saved response: media cleanup intent %q disappeared before activation", assetID)
	}
	return nil
}

func (j *cleanupJournal) remove(ctx context.Context, assetID string) error {
	if j == nil || j.db == nil || strings.TrimSpace(assetID) == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := j.db.ExecContext(ctx, `DELETE FROM saved_response_media_cleanup WHERE asset_id = ?`, assetID); err != nil {
		return fmt.Errorf("saved response: remove media cleanup %q: %w", assetID, err)
	}
	return nil
}

func (j *cleanupJournal) due(ctx context.Context, limit int) ([]cleanupItem, error) {
	if j == nil || j.db == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = defaultCleanupBatch
	}
	if limit > maxCleanupBatch {
		limit = maxCleanupBatch
	}
	rows, err := j.db.QueryContext(ctx, `
		SELECT asset_id, attempts
		FROM saved_response_media_cleanup
		WHERE next_attempt_at <= ?
		ORDER BY next_attempt_at ASC, created_at ASC
		LIMIT ?
	`, time.Now().UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("saved response: list due media cleanup: %w", err)
	}
	defer rows.Close()

	items := make([]cleanupItem, 0, limit)
	for rows.Next() {
		var item cleanupItem
		if err := rows.Scan(&item.AssetID, &item.Attempts); err != nil {
			return nil, fmt.Errorf("saved response: scan media cleanup: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("saved response: iterate media cleanup: %w", err)
	}
	return items, nil
}

func (j *cleanupJournal) recordFailure(ctx context.Context, item cleanupItem, cause error) error {
	if j == nil || j.db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	attempts := item.Attempts + 1
	message := ""
	if cause != nil {
		message = cause.Error()
		if len(message) > maxCleanupErrorLen {
			message = message[:maxCleanupErrorLen]
		}
	}
	now := time.Now().UTC()
	next := now.Add(cleanupRetryDelay(attempts))
	res, err := j.db.ExecContext(ctx, `
		UPDATE saved_response_media_cleanup
		SET attempts = ?, last_error = ?, next_attempt_at = ?, updated_at = ?
		WHERE asset_id = ?
	`, attempts, message, next, now, item.AssetID)
	if err != nil {
		return fmt.Errorf("saved response: record media cleanup failure %q: %w", item.AssetID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("saved response: media cleanup intent %q disappeared", item.AssetID)
	}
	return nil
}

func cleanupRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := min(attempt-1, 9)
	delay := 5 * time.Second * time.Duration(1<<shift)
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}

func (j *cleanupJournal) referenced(ctx context.Context, assetID string) (bool, error) {
	if j == nil || j.db == nil || strings.TrimSpace(assetID) == "" {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, source := range []struct {
		table  string
		column string
	}{
		{table: "notes", column: "media_asset_id"},
		{table: "filters", column: "media_asset_id"},
	} {
		exists, err := j.columnExists(ctx, source.table, source.column)
		if err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		var marker int
		query := fmt.Sprintf("SELECT 1 FROM %s WHERE %s = ? LIMIT 1", source.table, source.column)
		err = j.db.QueryRowContext(ctx, query, assetID).Scan(&marker)
		switch {
		case err == nil:
			return true, nil
		case err == sql.ErrNoRows:
			continue
		default:
			return false, fmt.Errorf("saved response: inspect %s media reference: %w", source.table, err)
		}
	}
	return false, nil
}

func (j *cleanupJournal) columnExists(ctx context.Context, table, column string) (bool, error) {
	query := fmt.Sprintf("PRAGMA table_info(%s)", table)
	rows, err := j.db.QueryContext(ctx, query)
	if err != nil {
		return false, fmt.Errorf("saved response: inspect table %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func (j *cleanupJournal) pendingCount(ctx context.Context) (int, error) {
	if j == nil || j.db == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var count int
	if err := j.db.QueryRowContext(ctx, `SELECT count(*) FROM saved_response_media_cleanup`).Scan(&count); err != nil {
		return 0, fmt.Errorf("saved response: count pending media cleanup: %w", err)
	}
	return count, nil
}
