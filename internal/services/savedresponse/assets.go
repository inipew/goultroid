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
	defaultPersistentReconcileBatch = 128
	maxPersistentReconcileBatch     = 1024
	persistentMediaOrphanGrace      = time.Minute
)

type assetLedger struct {
	db *database.DB
}

func newAssetLedger(db *database.DB) *assetLedger {
	if db == nil {
		return nil
	}
	return &assetLedger{db: db}
}

func (l *assetLedger) register(ctx context.Context, assetID string) error {
	if l == nil || l.db == nil {
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
	if _, err := l.db.ExecContext(ctx, `
		INSERT INTO saved_response_media_assets (asset_id, registered_at, last_seen_at)
		VALUES (?, ?, ?)
		ON CONFLICT(asset_id) DO NOTHING
	`, assetID, now, now); err != nil {
		return fmt.Errorf("saved response: register persistent media asset %q: %w", assetID, err)
	}
	return nil
}

func (l *assetLedger) markSeen(ctx context.Context, assetID string) error {
	if l == nil || l.db == nil {
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
	if _, err := l.db.ExecContext(ctx, `
		INSERT INTO saved_response_media_assets (asset_id, registered_at, last_seen_at)
		VALUES (?, ?, ?)
		ON CONFLICT(asset_id) DO UPDATE SET last_seen_at = excluded.last_seen_at
	`, assetID, now, now); err != nil {
		return fmt.Errorf("saved response: mark persistent media asset %q live: %w", assetID, err)
	}
	return nil
}

func (l *assetLedger) remove(ctx context.Context, assetID string) error {
	if l == nil || l.db == nil {
		return nil
	}
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := l.db.ExecContext(ctx, `DELETE FROM saved_response_media_assets WHERE asset_id = ?`, assetID); err != nil {
		return fmt.Errorf("saved response: remove persistent media asset %q: %w", assetID, err)
	}
	return nil
}

func (l *assetLedger) columnExists(ctx context.Context, table, column string) (bool, error) {
	if l == nil || l.db == nil {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := l.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
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

var persistentMediaReferenceSources = []struct {
	table  string
	column string
}{
	{table: "notes", column: "media_asset_id"},
	{table: "filters", column: "media_asset_id"},
}

func (l *assetLedger) backfillReferences(ctx context.Context) (int, error) {
	if l == nil || l.db == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	inserted := 0
	for _, source := range persistentMediaReferenceSources {
		exists, err := l.columnExists(ctx, source.table, source.column)
		if err != nil {
			return inserted, err
		}
		if !exists {
			continue
		}
		query := fmt.Sprintf(`
			INSERT OR IGNORE INTO saved_response_media_assets (asset_id, registered_at, last_seen_at)
			SELECT DISTINCT TRIM(%s), ?, ?
			FROM %s
			WHERE TRIM(%s) <> ''
		`, source.column, source.table, source.column)
		res, err := l.db.ExecContext(ctx, query, now, now)
		if err != nil {
			return inserted, fmt.Errorf("saved response: backfill %s media references: %w", source.table, err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return inserted, err
		}
		inserted += int(rows)
	}
	return inserted, nil
}

func (l *assetLedger) orphanCandidates(ctx context.Context, olderThan time.Time, limit int) ([]string, error) {
	if l == nil || l.db == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = defaultPersistentReconcileBatch
	}
	if limit > maxPersistentReconcileBatch {
		limit = maxPersistentReconcileBatch
	}

	clauses := []string{"a.registered_at <= ?"}
	for _, source := range persistentMediaReferenceSources {
		exists, err := l.columnExists(ctx, source.table, source.column)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		clauses = append(clauses, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM %s r WHERE r.%s = a.asset_id)",
			source.table,
			source.column,
		))
	}

	query := fmt.Sprintf(`
		SELECT a.asset_id
		FROM saved_response_media_assets a
		WHERE %s
		ORDER BY a.registered_at ASC, a.asset_id ASC
		LIMIT ?
	`, strings.Join(clauses, " AND "))
	args := []any{olderThan.UTC(), limit}
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("saved response: list persistent media orphans: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (l *assetLedger) count(ctx context.Context) (int, error) {
	if l == nil || l.db == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var count int
	if err := l.db.QueryRowContext(ctx, `SELECT count(*) FROM saved_response_media_assets`).Scan(&count); err != nil {
		return 0, fmt.Errorf("saved response: count persistent media assets: %w", err)
	}
	return count, nil
}
