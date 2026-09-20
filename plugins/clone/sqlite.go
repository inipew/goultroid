package clone

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
)

type SQLiteRepository struct {
	db            *database.DB
	registryReady bool
	registryErr   error
}

func NewSQLiteRepository(db *database.DB) *SQLiteRepository {
	ready, err := mediaregistry.SchemaReady(context.Background(), db)
	return &SQLiteRepository{db: db, registryReady: ready, registryErr: err}
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
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &state, nil
}

func (r *SQLiteRepository) SaveCloneState(ctx context.Context, state CloneState) error {
	if r.registryErr != nil {
		return fmt.Errorf("clone: inspect media registry schema: %w", r.registryErr)
	}
	if !r.registryReady {
		return r.saveCloneStateLegacy(ctx, state)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("clone: begin state save: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var previousPhoto string
	err = tx.QueryRowContext(ctx, `SELECT original_photo_path FROM clone_state WHERE owner_id = ?`, state.OwnerID).Scan(&previousPhoto)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("clone: inspect previous snapshot: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
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
		state.OriginalPhoto, state.ClonedPhoto, state.Active, state.UpdatedAt); err != nil {
		return err
	}

	nextID, nextManaged := cloneManagedAssetID(state.OriginalPhoto)
	if nextManaged {
		if err := mediaregistry.RegisterAssetWithExecutor(
			ctx,
			tx,
			cloneMediaAssetRegistration(nextID),
			cloneMediaReference(nextID, state.OwnerID),
		); err != nil {
			return fmt.Errorf("clone: mirror snapshot media registry: %w", err)
		}
	}
	previousID, previousManaged := cloneManagedAssetID(previousPhoto)
	if previousManaged && (!nextManaged || previousID != nextID) {
		if err := mediaregistry.RemoveReferenceWithExecutor(
			ctx,
			tx,
			cloneMediaReference(previousID, state.OwnerID),
		); err != nil {
			return fmt.Errorf("clone: remove previous snapshot reference: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("clone: commit state save: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) saveCloneStateLegacy(ctx context.Context, state CloneState) error {
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
	if r.registryErr != nil {
		return fmt.Errorf("clone: inspect media registry schema: %w", r.registryErr)
	}
	if !r.registryReady {
		_, err := r.db.ExecContext(ctx, `DELETE FROM clone_state WHERE owner_id = ?`, ownerID)
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("clone: begin state clear: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var photoRef string
	err = tx.QueryRowContext(ctx, `SELECT original_photo_path FROM clone_state WHERE owner_id = ?`, ownerID).Scan(&photoRef)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("clone: inspect snapshot before clear: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM clone_state WHERE owner_id = ?`, ownerID); err != nil {
		return err
	}
	if assetID, ok := cloneManagedAssetID(photoRef); ok {
		if err := mediaregistry.RemoveReferenceWithExecutor(ctx, tx, cloneMediaReference(assetID, ownerID)); err != nil {
			return fmt.Errorf("clone: remove snapshot reference: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("clone: commit state clear: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) RegisterCloneMediaAsset(ctx context.Context, assetID string) error {
	if r.registryErr != nil {
		return r.registryErr
	}
	if !r.registryReady {
		return nil
	}
	if err := mediaregistry.New(r.db).RegisterAsset(ctx, cloneMediaAssetRegistration(assetID)); err != nil {
		return fmt.Errorf("clone: register snapshot asset %q: %w", assetID, err)
	}
	return nil
}

func (r *SQLiteRepository) RemoveCloneMediaAsset(ctx context.Context, assetID string) error {
	if r.registryErr != nil {
		return r.registryErr
	}
	if !r.registryReady {
		return nil
	}
	if err := mediaregistry.New(r.db).RemoveOwnedAsset(ctx, assetID, cloneRegistryOwner); err != nil {
		return fmt.Errorf("clone: remove snapshot asset %q: %w", assetID, err)
	}
	return nil
}

var _ Repository = (*SQLiteRepository)(nil)
var _ mediaRegistryRepository = (*SQLiteRepository)(nil)
