package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GetSetting retrieves a setting by its scope, namespace, and key.
func (d *DB) GetSetting(ctx context.Context, scopeType string, scopeID int64, namespace, key string) (*SettingItem, error) {
	query := `
		SELECT scope_type, scope_id, namespace, key, value_type, value, updated_by, updated_at
		FROM settings
		WHERE scope_type = ? AND scope_id = ? AND namespace = ? AND key = ?`
	var item SettingItem
	err := d.QueryRowContext(ctx, query, scopeType, scopeID, namespace, key).Scan(
		&item.ScopeType,
		&item.ScopeID,
		&item.Namespace,
		&item.Key,
		&item.ValueType,
		&item.Value,
		&item.UpdatedBy,
		&item.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get setting (%s:%d:%s:%s): %w", scopeType, scopeID, namespace, key, err)
	}
	return &item, nil
}

// GetEffectiveSetting retrieves the highest-priority setting for a key across chat, user, and global scopes in a single query.
func (d *DB) GetEffectiveSetting(ctx context.Context, namespace, key string, chatID, userID int64) (*SettingItem, error) {
	query := `
		SELECT scope_type, scope_id, namespace, key, value_type, value, updated_by, updated_at
		FROM settings
		WHERE namespace = ? AND key = ?
		  AND (
		    (scope_type = 'chat' AND scope_id = ?)
		    OR (scope_type = 'user' AND scope_id = ?)
		    OR (scope_type = 'global' AND scope_id = 0)
		  )
		ORDER BY CASE scope_type WHEN 'chat' THEN 1 WHEN 'user' THEN 2 WHEN 'global' THEN 3 ELSE 4 END
		LIMIT 1`
	var item SettingItem
	err := d.QueryRowContext(ctx, query, namespace, key, chatID, userID).Scan(
		&item.ScopeType,
		&item.ScopeID,
		&item.Namespace,
		&item.Key,
		&item.ValueType,
		&item.Value,
		&item.UpdatedBy,
		&item.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get effective setting (%s:%s chat=%d user=%d): %w", namespace, key, chatID, userID, err)
	}
	return &item, nil
}

// SetSetting creates or updates a setting and appends a record to setting_changes audit log in a transaction.
func (d *DB) SetSetting(ctx context.Context, item *SettingItem) error {
	if item == nil {
		return errors.New("item cannot be nil")
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = time.Now().UTC()
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var oldVal string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE scope_type = ? AND scope_id = ? AND namespace = ? AND key = ?`,
		item.ScopeType, item.ScopeID, item.Namespace, item.Key).Scan(&oldVal)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to read previous setting value: %w", err)
	}

	upsertQuery := `
		INSERT INTO settings (scope_type, scope_id, namespace, key, value_type, value, updated_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope_type, scope_id, namespace, key) DO UPDATE SET
			value_type = excluded.value_type,
			value = excluded.value,
			updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`
	_, err = tx.ExecContext(ctx, upsertQuery,
		item.ScopeType, item.ScopeID, item.Namespace, item.Key, item.ValueType, item.Value, item.UpdatedBy, item.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to upsert setting: %w", err)
	}

	changeQuery := `
		INSERT INTO setting_changes (scope_type, scope_id, namespace, key, old_val, new_val, changed_by, changed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = tx.ExecContext(ctx, changeQuery,
		item.ScopeType, item.ScopeID, item.Namespace, item.Key, oldVal, item.Value, item.UpdatedBy, item.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to record setting change audit: %w", err)
	}

	outboxQuery := `
		INSERT INTO setting_outbox (scope_type, scope_id, namespace, key, old_val, new_val, changed_by, created_at, processed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`
	_, err = tx.ExecContext(ctx, outboxQuery,
		item.ScopeType, item.ScopeID, item.Namespace, item.Key, oldVal, item.Value, item.UpdatedBy, item.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to insert setting outbox: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit setting transaction: %w", err)
	}
	return nil
}

// SetSettingsBatch creates or updates multiple settings atomically within a single transaction.
func (d *DB) SetSettingsBatch(ctx context.Context, items []*SettingItem) error {
	if len(items) == 0 {
		return nil
	}
	now := time.Now().UTC()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin batch settings transaction: %w", err)
	}
	defer tx.Rollback()

	selectStmt, err := tx.PrepareContext(ctx, `SELECT value FROM settings WHERE scope_type = ? AND scope_id = ? AND namespace = ? AND key = ?`)
	if err != nil {
		return fmt.Errorf("prepare select statement: %w", err)
	}
	defer selectStmt.Close()

	upsertStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO settings (scope_type, scope_id, namespace, key, value_type, value, updated_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope_type, scope_id, namespace, key) DO UPDATE SET
			value_type = excluded.value_type,
			value = excluded.value,
			updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`)
	if err != nil {
		return fmt.Errorf("prepare upsert statement: %w", err)
	}
	defer upsertStmt.Close()

	changeStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO setting_changes (scope_type, scope_id, namespace, key, old_val, new_val, changed_by, changed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare audit statement: %w", err)
	}
	defer changeStmt.Close()

	outboxStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO setting_outbox (scope_type, scope_id, namespace, key, old_val, new_val, changed_by, created_at, processed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`)
	if err != nil {
		return fmt.Errorf("prepare outbox statement: %w", err)
	}
	defer outboxStmt.Close()

	for _, item := range items {
		if item == nil {
			continue
		}
		if item.UpdatedAt.IsZero() {
			item.UpdatedAt = now
		}

		var oldVal string
		err := selectStmt.QueryRowContext(ctx, item.ScopeType, item.ScopeID, item.Namespace, item.Key).Scan(&oldVal)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failed to read previous setting value for %s/%s: %w", item.Namespace, item.Key, err)
		}

		_, err = upsertStmt.ExecContext(ctx,
			item.ScopeType, item.ScopeID, item.Namespace, item.Key, item.ValueType, item.Value, item.UpdatedBy, item.UpdatedAt)
		if err != nil {
			return fmt.Errorf("failed to upsert setting %s/%s: %w", item.Namespace, item.Key, err)
		}

		_, err = changeStmt.ExecContext(ctx,
			item.ScopeType, item.ScopeID, item.Namespace, item.Key, oldVal, item.Value, item.UpdatedBy, item.UpdatedAt)
		if err != nil {
			return fmt.Errorf("failed to record setting change audit for %s/%s: %w", item.Namespace, item.Key, err)
		}

		_, err = outboxStmt.ExecContext(ctx,
			item.ScopeType, item.ScopeID, item.Namespace, item.Key, oldVal, item.Value, item.UpdatedBy, item.UpdatedAt)
		if err != nil {
			return fmt.Errorf("failed to insert setting outbox for %s/%s: %w", item.Namespace, item.Key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit batch settings transaction: %w", err)
	}
	return nil
}

// DeleteSetting removes a setting and records the deletion in the audit log in a transaction.
func (d *DB) DeleteSetting(ctx context.Context, scopeType string, scopeID int64, namespace, key string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var oldVal string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE scope_type = ? AND scope_id = ? AND namespace = ? AND key = ?`,
		scopeType, scopeID, namespace, key).Scan(&oldVal)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("failed to read setting before deletion: %w", err)
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM settings WHERE scope_type = ? AND scope_id = ? AND namespace = ? AND key = ?`,
		scopeType, scopeID, namespace, key)
	if err != nil {
		return fmt.Errorf("failed to delete setting: %w", err)
	}

	now := time.Now().UTC()
	changeQuery := `
		INSERT INTO setting_changes (scope_type, scope_id, namespace, key, old_val, new_val, changed_by, changed_at)
		VALUES (?, ?, ?, ?, ?, '', 0, ?)`
	_, err = tx.ExecContext(ctx, changeQuery, scopeType, scopeID, namespace, key, oldVal, now)
	if err != nil {
		return fmt.Errorf("failed to record setting deletion audit: %w", err)
	}

	outboxQuery := `
		INSERT INTO setting_outbox (scope_type, scope_id, namespace, key, old_val, new_val, changed_by, created_at, processed)
		VALUES (?, ?, ?, ?, ?, '', 0, ?, 0)`
	_, err = tx.ExecContext(ctx, outboxQuery, scopeType, scopeID, namespace, key, oldVal, now)
	if err != nil {
		return fmt.Errorf("failed to insert setting outbox for deletion: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit setting deletion: %w", err)
	}
	return nil
}

// ListPendingOutbox retrieves unprocessed outbox entries for durable publishing.
func (d *DB) ListPendingOutbox(ctx context.Context, limit int) ([]SettingOutboxEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx, `
		SELECT id, scope_type, scope_id, namespace, key, old_val, new_val, changed_by, created_at, processed
		FROM setting_outbox
		WHERE processed = 0
		ORDER BY created_at ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending outbox: %w", err)
	}
	defer rows.Close()
	var entries []SettingOutboxEntry
	for rows.Next() {
		var e SettingOutboxEntry
		var processed int
		if err := rows.Scan(&e.ID, &e.ScopeType, &e.ScopeID, &e.Namespace, &e.Key, &e.OldVal, &e.NewVal, &e.ChangedBy, &e.CreatedAt, &processed); err != nil {
			return nil, fmt.Errorf("failed to scan outbox entry: %w", err)
		}
		e.Processed = processed != 0
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// MarkOutboxProcessed marks an outbox entry as processed.
func (d *DB) MarkOutboxProcessed(ctx context.Context, id int64) error {
	_, err := d.ExecContext(ctx, `UPDATE setting_outbox SET processed = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to mark outbox %d processed: %w", id, err)
	}
	return nil
}

// ListSettings returns all settings for a scope, optionally filtered by namespace (if non-empty).
func (d *DB) ListSettings(ctx context.Context, scopeType string, scopeID int64, namespace string) ([]SettingItem, error) {
	var rows *sql.Rows
	var err error
	if namespace != "" {
		query := `
			SELECT scope_type, scope_id, namespace, key, value_type, value, updated_by, updated_at
			FROM settings
			WHERE scope_type = ? AND scope_id = ? AND namespace = ?
			ORDER BY key ASC`
		rows, err = d.QueryContext(ctx, query, scopeType, scopeID, namespace)
	} else {
		query := `
			SELECT scope_type, scope_id, namespace, key, value_type, value, updated_by, updated_at
			FROM settings
			WHERE scope_type = ? AND scope_id = ?
			ORDER BY namespace ASC, key ASC`
		rows, err = d.QueryContext(ctx, query, scopeType, scopeID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query settings: %w", err)
	}
	defer rows.Close()

	var items []SettingItem
	for rows.Next() {
		var item SettingItem
		if err := rows.Scan(&item.ScopeType, &item.ScopeID, &item.Namespace, &item.Key, &item.ValueType, &item.Value, &item.UpdatedBy, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan setting item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// GetSettingHistory retrieves the audit history for a setting.
func (d *DB) GetSettingHistory(ctx context.Context, namespace, key string, limit int) ([]SettingChangeRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT id, scope_type, scope_id, namespace, key, old_val, new_val, changed_by, changed_at
		FROM setting_changes
		WHERE namespace = ? AND key = ?
		ORDER BY changed_at DESC, id DESC
		LIMIT ?`
	rows, err := d.QueryContext(ctx, query, namespace, key, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query setting history: %w", err)
	}
	defer rows.Close()
	var records []SettingChangeRecord
	for rows.Next() {
		var r SettingChangeRecord
		if err := rows.Scan(&r.ID, &r.ScopeType, &r.ScopeID, &r.Namespace, &r.Key, &r.OldVal, &r.NewVal, &r.ChangedBy, &r.ChangedAt); err != nil {
			return nil, fmt.Errorf("failed to scan setting change record: %w", err)
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}
