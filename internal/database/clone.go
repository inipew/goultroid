package database

import (
	"context"
	"database/sql"
	"time"
)

// CloneState stores the self-profile snapshot required to safely revert a clone operation.
type CloneState struct {
	OwnerID       int64
	OriginalFirst string
	OriginalLast  string
	OriginalBio   string
	OriginalPhoto string
	Active        bool
	UpdatedAt     time.Time
}

// CloneRepository persists clone/revert state independently from generic settings.
type CloneRepository interface {
	GetCloneState(ctx context.Context, ownerID int64) (*CloneState, error)
	SaveCloneState(ctx context.Context, state CloneState) error
	ClearCloneState(ctx context.Context, ownerID int64) error
}

func (d *DB) GetCloneState(ctx context.Context, ownerID int64) (*CloneState, error) {
	var s CloneState
	err := d.QueryRowContext(ctx, `
		SELECT owner_id, original_first_name, original_last_name, original_bio,
		       original_photo_path, active, updated_at
		FROM clone_state WHERE owner_id = ?`, ownerID).
		Scan(&s.OwnerID, &s.OriginalFirst, &s.OriginalLast, &s.OriginalBio,
			&s.OriginalPhoto, &s.Active, &s.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func (d *DB) SaveCloneState(ctx context.Context, state CloneState) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO clone_state (
			owner_id, original_first_name, original_last_name, original_bio,
			original_photo_path, active, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner_id) DO UPDATE SET
			original_first_name = excluded.original_first_name,
			original_last_name = excluded.original_last_name,
			original_bio = excluded.original_bio,
			original_photo_path = excluded.original_photo_path,
			active = excluded.active,
			updated_at = excluded.updated_at`,
		state.OwnerID, state.OriginalFirst, state.OriginalLast, state.OriginalBio,
		state.OriginalPhoto, state.Active, state.UpdatedAt)
	return err
}

func (d *DB) ClearCloneState(ctx context.Context, ownerID int64) error {
	_, err := d.ExecContext(ctx, `DELETE FROM clone_state WHERE owner_id = ?`, ownerID)
	return err
}

var _ CloneRepository = (*DB)(nil)
