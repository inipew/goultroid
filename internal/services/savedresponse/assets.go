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
	defaultPersistentReconcileBatch = 32
	maxPersistentReconcileBatch     = 128
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

type mediaReferenceSource struct {
	table      string
	column     string
	textColumn string
}

var persistentMediaReferenceSources = []mediaReferenceSource{
	{table: "notes", column: "media_asset_id", textColumn: "content"},
	{table: "filters", column: "media_asset_id", textColumn: "reply_text"},
}

func normalizePersistentReconcileLimit(limit int) int {
	if limit <= 0 {
		return defaultPersistentReconcileBatch
	}
	return min(limit, maxPersistentReconcileBatch)
}

func (l *assetLedger) resolveReferenceSources(ctx context.Context) ([]mediaReferenceSource, bool, error) {
	if l == nil || l.db == nil {
		return nil, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	resolved := make([]mediaReferenceSource, 0, len(persistentMediaReferenceSources))
	complete := true
	for _, source := range persistentMediaReferenceSources {
		sourceComplete := true
		for _, column := range []string{
			source.column,
			source.textColumn,
			"media_type",
			"media_name",
			"media_mime",
		} {
			exists, err := l.columnExists(ctx, source.table, column)
			if err != nil {
				return nil, false, err
			}
			if !exists {
				sourceComplete = false
				complete = false
				break
			}
		}
		if sourceComplete {
			resolved = append(resolved, source)
		}
	}
	return resolved, complete && len(resolved) == len(persistentMediaReferenceSources), nil
}

func (l *assetLedger) backfillSource(
	ctx context.Context,
	source mediaReferenceSource,
	now time.Time,
	limit int,
) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	query := fmt.Sprintf(`
		INSERT OR IGNORE INTO saved_response_media_assets (asset_id, registered_at, last_seen_at)
		SELECT DISTINCT TRIM(r.%s), ?, ?
		FROM %s r
		WHERE TRIM(r.%s) <> ''
		  AND NOT EXISTS (
			SELECT 1 FROM saved_response_media_assets a
			WHERE a.asset_id = TRIM(r.%s)
		  )
		LIMIT ?
	`, source.column, source.table, source.column, source.column)
	res, err := l.db.ExecContext(ctx, query, now, now, limit)
	if err != nil {
		return 0, fmt.Errorf("saved response: backfill %s media references: %w", source.table, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rows), nil
}

func (l *assetLedger) backfillReferencesFromSources(
	ctx context.Context,
	sources []mediaReferenceSource,
	limit int,
) (int, error) {
	if l == nil || l.db == nil || len(sources) == 0 {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	limit = normalizePersistentReconcileLimit(limit)
	now := time.Now().UTC()
	inserted := 0

	// Give every known reference source a fair first share so a large Notes
	// table cannot indefinitely starve legacy Filters references (or vice versa).
	quota := max(1, limit/len(sources))
	for _, source := range sources {
		remaining := limit - inserted
		if remaining <= 0 {
			break
		}
		n, err := l.backfillSource(ctx, source, now, min(quota, remaining))
		if err != nil {
			return inserted, err
		}
		inserted += n
	}

	// Spend unused budget on any source that still has unseen references.
	for inserted < limit {
		progress := 0
		for _, source := range sources {
			remaining := limit - inserted
			if remaining <= 0 {
				break
			}
			n, err := l.backfillSource(ctx, source, now, remaining)
			if err != nil {
				return inserted, err
			}
			inserted += n
			progress += n
		}
		if progress == 0 {
			break
		}
	}
	return inserted, nil
}

func (l *assetLedger) backfillReferences(ctx context.Context, limit int) (int, error) {
	sources, _, err := l.resolveReferenceSources(ctx)
	if err != nil {
		return 0, err
	}
	return l.backfillReferencesFromSources(ctx, sources, limit)
}

func (l *assetLedger) referencedCandidatesFromSources(
	ctx context.Context,
	sources []mediaReferenceSource,
	limit int,
) ([]string, error) {
	if l == nil || l.db == nil || len(sources) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	limit = normalizePersistentReconcileLimit(limit)

	clauses := make([]string, 0, len(sources))
	for _, source := range sources {
		clauses = append(clauses, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM %s r WHERE TRIM(r.%s) = a.asset_id)",
			source.table,
			source.column,
		))
	}
	query := fmt.Sprintf(`
		SELECT a.asset_id
		FROM saved_response_media_assets a
		WHERE %s
		ORDER BY a.last_seen_at ASC, a.asset_id ASC
		LIMIT ?
	`, strings.Join(clauses, " OR "))
	rows, err := l.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("saved response: list referenced persistent media: %w", err)
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

func (l *assetLedger) repairMissingReferencesFromSources(
	ctx context.Context,
	sources []mediaReferenceSource,
	assetID string,
) (detached, removed int, err error) {
	if l == nil || l.db == nil || len(sources) == 0 {
		return 0, 0, nil
	}
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return 0, 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("saved response: begin missing media repair: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, source := range sources {
		deleteQuery := fmt.Sprintf(
			"DELETE FROM %s WHERE TRIM(%s) = ? AND TRIM(%s) = ''",
			source.table,
			source.column,
			source.textColumn,
		)
		res, execErr := tx.ExecContext(ctx, deleteQuery, assetID)
		if execErr != nil {
			return detached, removed, fmt.Errorf("saved response: remove media-only %s response: %w", source.table, execErr)
		}
		rows, rowsErr := res.RowsAffected()
		if rowsErr != nil {
			return detached, removed, rowsErr
		}
		removed += int(rows)

		updateQuery := fmt.Sprintf(`
			UPDATE %s
			SET media_asset_id = '', media_type = '', media_name = '', media_mime = ''
			WHERE TRIM(%s) = ?
		`, source.table, source.column)
		res, execErr = tx.ExecContext(ctx, updateQuery, assetID)
		if execErr != nil {
			return detached, removed, fmt.Errorf("saved response: detach missing %s media: %w", source.table, execErr)
		}
		rows, rowsErr = res.RowsAffected()
		if rowsErr != nil {
			return detached, removed, rowsErr
		}
		detached += int(rows)
	}
	if err := tx.Commit(); err != nil {
		return detached, removed, fmt.Errorf("saved response: commit missing media repair: %w", err)
	}
	return detached, removed, nil
}

func (l *assetLedger) orphanCandidatesFromSources(
	ctx context.Context,
	sources []mediaReferenceSource,
	schemaComplete bool,
	limit int,
) ([]string, error) {
	if l == nil || l.db == nil || !schemaComplete {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	limit = normalizePersistentReconcileLimit(limit)

	clauses := make([]string, 0, len(sources))
	for _, source := range sources {
		clauses = append(clauses, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM %s r WHERE TRIM(r.%s) = a.asset_id)",
			source.table,
			source.column,
		))
	}
	if len(clauses) == 0 {
		return nil, nil
	}
	query := fmt.Sprintf(`
		SELECT a.asset_id
		FROM saved_response_media_assets a
		WHERE %s
		ORDER BY a.registered_at ASC, a.asset_id ASC
		LIMIT ?
	`, strings.Join(clauses, " AND "))
	rows, err := l.db.QueryContext(ctx, query, limit)
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
