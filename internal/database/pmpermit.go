package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

var warnDBSync sync.Mutex

// PMPermitRecord represents the security permit state of a Telegram user in PM.
type PMPermitRecord struct {
	UserID      int64      `json:"user_id"`
	Status      string     `json:"status"`
	FirstSeenAt time.Time  `json:"first_seen_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	WarnCount   int        `json:"warn_count"`
}

// GetPMRecord retrieves the PM permit record for a specific user.
func (db *DB) GetPMRecord(ctx context.Context, userID int64) (*PMPermitRecord, error) {
	query := `
		SELECT user_id, status, first_seen_at, last_seen_at, expires_at, reason, warn_count
		FROM pm_permit_records
		WHERE user_id = ?
	`
	row := db.QueryRowContext(ctx, query, userID)

	var rec PMPermitRecord
	var expiresAt sql.NullTime
	var reason sql.NullString

	err := row.Scan(
		&rec.UserID,
		&rec.Status,
		&rec.FirstSeenAt,
		&rec.LastSeenAt,
		&expiresAt,
		&reason,
		&rec.WarnCount,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to scan pm permit record: %w", err)
	}

	if expiresAt.Valid {
		rec.ExpiresAt = &expiresAt.Time
	}
	if reason.Valid {
		rec.Reason = reason.String
	}

	return &rec, nil
}

// SetPMStatus updates or inserts a user's PM permit status.
func (db *DB) SetPMStatus(ctx context.Context, userID int64, status string, reason string, expiresAt *time.Time) error {
	now := time.Now().UTC()
	query := `
		INSERT INTO pm_permit_records (user_id, status, first_seen_at, last_seen_at, expires_at, reason, warn_count)
		VALUES (?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(user_id) DO UPDATE SET
			status = excluded.status,
			last_seen_at = excluded.last_seen_at,
			expires_at = excluded.expires_at,
			reason = excluded.reason;
	`
	var expVal any = nil
	if expiresAt != nil {
		expVal = expiresAt.UTC()
	}

	_, err := db.ExecContext(ctx, query, userID, status, now, now, expVal, reason)
	if err != nil {
		return fmt.Errorf("failed to set pm permit status: %w", err)
	}
	return nil
}

// IncrementPMWarn increments the warning counter for an unapproved user and updates last_seen_at.
func (db *DB) IncrementPMWarn(ctx context.Context, userID int64) (int, error) {
	now := time.Now().UTC()
	query := `
		INSERT INTO pm_permit_records (user_id, status, first_seen_at, last_seen_at, warn_count)
		VALUES (?, 'pending', ?, ?, 1)
		ON CONFLICT(user_id) DO UPDATE SET
			last_seen_at = excluded.last_seen_at,
			warn_count = pm_permit_records.warn_count + 1
		RETURNING warn_count;
	`
	var newCount int
	err := db.QueryRowContext(ctx, query, userID, now, now).Scan(&newCount)
	if err != nil {
		return 0, fmt.Errorf("failed to increment pm warn count: %w", err)
	}
	return newCount, nil
}

// ResetPMWarn resets a user's warning counter to zero.
func (db *DB) ResetPMWarn(ctx context.Context, userID int64) error {
	query := `UPDATE pm_permit_records SET warn_count = 0 WHERE user_id = ?;`
	_, err := db.ExecContext(ctx, query, userID)
	if err != nil {
		return fmt.Errorf("failed to reset pm warn: %w", err)
	}
	return nil
}

// GetWarnMsgIDs retrieves stored warning message IDs for a user.
func (db *DB) GetWarnMsgIDs(ctx context.Context, userID int64) ([]int, error) {
	var raw sql.NullString
	err := db.QueryRowContext(ctx, "SELECT warn_msg_ids FROM pm_permit_records WHERE user_id = ?", userID).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get warn msg ids: %w", err)
	}
	if !raw.Valid || raw.String == "" || raw.String == "[]" {
		return nil, nil
	}
	var ids []int
	if err := json.Unmarshal([]byte(raw.String), &ids); err != nil {
		// corrupt JSON → treat as empty, log via caller
		return nil, nil
	}
	return ids, nil
}

// AddWarnMsgID appends a warning message ID for a user, capped at 20.
func (db *DB) AddWarnMsgID(ctx context.Context, userID int64, msgID int) error {
	warnDBSync.Lock()
	defer warnDBSync.Unlock()

	ids, _ := db.GetWarnMsgIDs(ctx, userID)
	ids = append(ids, msgID)
	if len(ids) > 20 {
		ids = ids[len(ids)-20:]
	}
	b, _ := json.Marshal(ids)
	_, err := db.ExecContext(ctx, "UPDATE pm_permit_records SET warn_msg_ids = ? WHERE user_id = ?", string(b), userID)
	if err != nil {
		return fmt.Errorf("failed to add warn msg id: %w", err)
	}
	return nil
}

// ClearWarnMsgIDs clears stored warning message IDs for a user.
func (db *DB) ClearWarnMsgIDs(ctx context.Context, userID int64) error {
	warnDBSync.Lock()
	defer warnDBSync.Unlock()

	_, err := db.ExecContext(ctx, "UPDATE pm_permit_records SET warn_msg_ids = '[]' WHERE user_id = ?", userID)
	if err != nil {
		return fmt.Errorf("failed to clear warn msg ids: %w", err)
	}
	return nil
}

// ListPMRecords lists PM permit records filtered by status ("" for all), ordered by last_seen_at DESC.
func (db *DB) ListPMRecords(ctx context.Context, status string, limit, offset int) ([]*PMPermitRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var rows *sql.Rows
	var err error
	if status != "" {
		query := `
			SELECT user_id, status, first_seen_at, last_seen_at, expires_at, reason, warn_count
			FROM pm_permit_records
			WHERE status = ?
			ORDER BY last_seen_at DESC
			LIMIT ? OFFSET ?;
		`
		rows, err = db.QueryContext(ctx, query, status, limit, offset)
	} else {
		query := `
			SELECT user_id, status, first_seen_at, last_seen_at, expires_at, reason, warn_count
			FROM pm_permit_records
			ORDER BY last_seen_at DESC
			LIMIT ? OFFSET ?;
		`
		rows, err = db.QueryContext(ctx, query, limit, offset)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list pm records: %w", err)
	}
	defer rows.Close()

	var records []*PMPermitRecord
	for rows.Next() {
		var rec PMPermitRecord
		var expiresAt sql.NullTime
		var reason sql.NullString
		if err := rows.Scan(&rec.UserID, &rec.Status, &rec.FirstSeenAt, &rec.LastSeenAt, &expiresAt, &reason, &rec.WarnCount); err != nil {
			return nil, fmt.Errorf("failed to scan pm record: %w", err)
		}
		if expiresAt.Valid {
			rec.ExpiresAt = &expiresAt.Time
		}
		if reason.Valid {
			rec.Reason = reason.String
		}
		records = append(records, &rec)
	}
	return records, nil
}

// CountPMRecords returns the total counts of pending, approved, and blocked PM records.
func (db *DB) CountPMRecords(ctx context.Context) (pending, approved, blocked int, err error) {
	query := `
		SELECT
			COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'approved' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'blocked' THEN 1 ELSE 0 END), 0)
		FROM pm_permit_records;
	`
	err = db.QueryRowContext(ctx, query).Scan(&pending, &approved, &blocked)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to count pm records: %w", err)
	}
	return pending, approved, blocked, nil
}

// GetUserLogSetting retrieves a key-value setting for user logs.
func (db *DB) GetUserLogSetting(ctx context.Context, key string) (string, error) {
	var val string
	err := db.QueryRowContext(ctx, "SELECT val FROM user_log_settings WHERE key = ?", key).Scan(&val)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("failed to get user log setting: %w", err)
	}
	return val, nil
}

// SetUserLogSetting stores or updates a key-value setting for user logs.
func (db *DB) SetUserLogSetting(ctx context.Context, key, val string) error {
	query := `
		INSERT INTO user_log_settings (key, val)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET val = excluded.val;
	`
	_, err := db.ExecContext(ctx, query, key, val)
	if err != nil {
		return fmt.Errorf("failed to set user log setting: %w", err)
	}
	return nil
}
