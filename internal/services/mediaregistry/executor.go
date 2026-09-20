package mediaregistry

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

// SchemaReady reports whether the global media registry schema is available.
// It is used by compatibility callers that may be constructed in tests or
// standalone feature environments before application-wide migrations run.
func SchemaReady(ctx context.Context, db database.SQLExecutor) (bool, error) {
	if db == nil {
		return false, ErrNilDatabase
	}
	ctx = normalizeContext(ctx)
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name IN ('media_assets', 'media_asset_references')
	`).Scan(&count); err != nil {
		return false, fmt.Errorf("media registry: inspect schema: %w", err)
	}
	return count == 2, nil
}

// RegisterAssetWithExecutor mirrors RegisterAsset using a caller-owned SQL
// executor. Callers that need the feature row and media registry mutation to be
// atomic should pass the same transaction for both operations.
func RegisterAssetWithExecutor(
	ctx context.Context,
	exec database.SQLExecutor,
	reg AssetRegistration,
	refs ...Reference,
) error {
	if exec == nil {
		return ErrNilDatabase
	}
	ctx = normalizeContext(ctx)
	var err error
	reg, err = normalizeRegistration(reg)
	if err != nil {
		return err
	}
	normalizedRefs := make([]Reference, 0, len(refs))
	for _, ref := range refs {
		ref, err = normalizeReference(ref)
		if err != nil {
			return err
		}
		if ref.AssetID != reg.AssetID {
			return fmt.Errorf(
				"%w: reference asset %q does not match registration %q",
				ErrInvalidReference,
				ref.AssetID,
				reg.AssetID,
			)
		}
		normalizedRefs = append(normalizedRefs, ref)
	}

	now := time.Now().UTC()
	res, err := exec.ExecContext(ctx, `
		INSERT INTO media_assets (
			asset_id, producer, owner, lifecycle, registered_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(asset_id) DO UPDATE SET
			lifecycle = excluded.lifecycle,
			updated_at = excluded.updated_at
		WHERE media_assets.producer = excluded.producer
		  AND media_assets.owner = excluded.owner
	`, reg.AssetID, reg.Producer, reg.Owner, reg.Lifecycle, now, now)
	if err != nil {
		return fmt.Errorf("media registry: register asset %q: %w", reg.AssetID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("media registry: inspect registration %q: %w", reg.AssetID, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: asset %q", ErrOwnershipConflict, reg.AssetID)
	}
	for _, ref := range normalizedRefs {
		if err := upsertReference(ctx, exec, ref, now); err != nil {
			return err
		}
	}
	return nil
}

// RemoveReferenceWithExecutor removes one exact reference using a caller-owned
// transaction or database handle.
func RemoveReferenceWithExecutor(ctx context.Context, exec database.SQLExecutor, ref Reference) error {
	if exec == nil {
		return ErrNilDatabase
	}
	ctx = normalizeContext(ctx)
	var err error
	ref, err = normalizeReference(ref)
	if err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx, `
		DELETE FROM media_asset_references
		WHERE asset_id = ? AND subsystem = ? AND reference_kind = ? AND reference_key = ?
	`, ref.AssetID, ref.Subsystem, ref.Kind, ref.Key); err != nil {
		return fmt.Errorf("media registry: remove reference for asset %q: %w", ref.AssetID, err)
	}
	return nil
}

// RemoveOwnedAssetWithExecutor removes only metadata owned by the expected
// subsystem. Physical storage is deliberately outside this operation.
func RemoveOwnedAssetWithExecutor(
	ctx context.Context,
	exec database.SQLExecutor,
	assetID string,
	owner string,
) error {
	if exec == nil {
		return ErrNilDatabase
	}
	ctx = normalizeContext(ctx)
	assetID = strings.TrimSpace(assetID)
	owner = strings.TrimSpace(owner)
	if assetID == "" || owner == "" {
		return ErrInvalidAsset
	}
	if _, err := exec.ExecContext(ctx, `
		DELETE FROM media_assets
		WHERE asset_id = ? AND owner = ?
		  AND NOT EXISTS (
			SELECT 1 FROM media_asset_references WHERE asset_id = ?
		  )
	`, assetID, owner, assetID); err != nil {
		return fmt.Errorf("media registry: remove owned asset %q: %w", assetID, err)
	}
	return nil
}
