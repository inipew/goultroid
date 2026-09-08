package clone

import (
	"context"
	"database/sql"

	"github.com/inipew/goultroid/internal/database"
)

type SQLiteRepository struct {
	db *database.DB
}

func NewSQLiteRepository(db *database.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

func (r *SQLiteRepository) GetCloneState(ctx context.Context, ownerID int64) (*CloneState, error) {
	var state CloneState
	err := r.db.QueryRowContext(ctx, `
		SELECT owner_id, original_first_name, original_last_name, original_bio,
		       original_photo_path, cloned_photo, active, updated_at
		FROM clone_state WHERE owner_id = ?`, ownerID).
		Scan(&state.OwnerID, &state.OriginalFirst, &state.OriginalLast, &state.OriginalBio,
			&state.OriginalPhoto, &state.ClonedPhoto, &state.Active, &state.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &state, nil
}

func (r *SQLiteRepository) SaveCloneState(ctx context.Context, state CloneState) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO clone_state (
			owner_id, original_first_name, original_last_name, original_bio,
			original_photo_path, cloned_photo, active, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner_id) DO UPDATE SET
			original_first_name = excluded.original_first_name,
			original_last_name = excluded.original_last_name,
			original_bio = excluded.original_bio,
			original_photo_path = excluded.original_photo_path,
			cloned_photo = excluded.cloned_photo,
			active = excluded.active,
			updated_at = excluded.updated_at`,
		state.OwnerID, state.OriginalFirst, state.OriginalLast, state.OriginalBio,
		state.OriginalPhoto, state.ClonedPhoto, state.Active, state.UpdatedAt)
	return err
}

func (r *SQLiteRepository) ClearCloneState(ctx context.Context, ownerID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM clone_state WHERE owner_id = ?`, ownerID)
	return err
}

var _ Repository = (*SQLiteRepository)(nil)
