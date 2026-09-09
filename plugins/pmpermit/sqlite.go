package pmpermit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/database"
	pmpermitSvc "github.com/inipew/goultroid/internal/services/pmpermit"
)

var warnDBSync sync.Mutex

// SQLiteRepository implements pmpermit.Repository using SQLite.
type SQLiteRepository struct {
	db database.SQLExecutor
}

func NewSQLiteRepository(db database.SQLExecutor) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

var _ pmpermitSvc.Repository = (*SQLiteRepository)(nil)

func (r *SQLiteRepository) GetPMRecord(ctx context.Context, userID int64) (*pmpermitSvc.PMPermitRecord, error) {
	query := `
		SELECT user_id, status, first_seen_at, last_seen_at, expires_at, reason, warn_count
		FROM pm_permit_records
		WHERE user_id = ?
	`
	row := r.db.QueryRowContext(ctx, query, userID)

	var rec pmpermitSvc.PMPermitRecord
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

func (r *SQLiteRepository) SetPMStatus(ctx context.Context, userID int64, status string, reason string, expiresAt *time.Time) error {
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

	_, err := r.db.ExecContext(ctx, query, userID, status, now, now, expVal, reason)
	if err != nil {
		return fmt.Errorf("failed to set pm permit status: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) IncrementPMWarn(ctx context.Context, userID int64) (int, error) {
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
	err := r.db.QueryRowContext(ctx, query, userID, now, now).Scan(&newCount)
	if err != nil {
		return 0, fmt.Errorf("failed to increment pm warn count: %w", err)
	}
	return newCount, nil
}

func (r *SQLiteRepository) ResetPMWarn(ctx context.Context, userID int64) error {
	query := `UPDATE pm_permit_records SET warn_count = 0 WHERE user_id = ?;`
	_, err := r.db.ExecContext(ctx, query, userID)
	if err != nil {
		return fmt.Errorf("failed to reset pm warn: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) GetWarnMsgIDs(ctx context.Context, userID int64) ([]int, error) {
	var raw sql.NullString
	err := r.db.QueryRowContext(ctx, "SELECT warn_msg_ids FROM pm_permit_records WHERE user_id = ?", userID).Scan(&raw)
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
		return nil, nil
	}
	return ids, nil
}

func (r *SQLiteRepository) AddWarnMsgID(ctx context.Context, userID int64, msgID int) error {
	warnDBSync.Lock()
	defer warnDBSync.Unlock()

	ids, _ := r.GetWarnMsgIDs(ctx, userID)
	ids = append(ids, msgID)
	if len(ids) > 20 {
		ids = ids[len(ids)-20:]
	}
	b, _ := json.Marshal(ids)
	_, err := r.db.ExecContext(ctx, "UPDATE pm_permit_records SET warn_msg_ids = ? WHERE user_id = ?", string(b), userID)
	if err != nil {
		return fmt.Errorf("failed to add warn msg id: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) ClearWarnMsgIDs(ctx context.Context, userID int64) error {
	warnDBSync.Lock()
	defer warnDBSync.Unlock()

	_, err := r.db.ExecContext(ctx, "UPDATE pm_permit_records SET warn_msg_ids = '[]' WHERE user_id = ?", userID)
	if err != nil {
		return fmt.Errorf("failed to clear warn msg ids: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) ListPMRecords(ctx context.Context, status string, limit, offset int) ([]*pmpermitSvc.PMPermitRecord, error) {
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
		rows, err = r.db.QueryContext(ctx, query, status, limit, offset)
	} else {
		query := `
			SELECT user_id, status, first_seen_at, last_seen_at, expires_at, reason, warn_count
			FROM pm_permit_records
			ORDER BY last_seen_at DESC
			LIMIT ? OFFSET ?;
		`
		rows, err = r.db.QueryContext(ctx, query, limit, offset)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list pm records: %w", err)
	}
	defer rows.Close()

	var records []*pmpermitSvc.PMPermitRecord
	for rows.Next() {
		var rec pmpermitSvc.PMPermitRecord
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate pm records: %w", err)
	}
	return records, nil
}

func (r *SQLiteRepository) CountPMRecords(ctx context.Context) (pending, approved, blocked int, err error) {
	query := `
		SELECT
			COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'approved' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'blocked' THEN 1 ELSE 0 END), 0)
		FROM pm_permit_records;
	`
	err = r.db.QueryRowContext(ctx, query).Scan(&pending, &approved, &blocked)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to count pm records: %w", err)
	}
	return pending, approved, blocked, nil
}
