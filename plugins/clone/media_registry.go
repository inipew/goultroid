package clone

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
)

const (
	cloneRegistryProducer  = "clone.snapshot"
	cloneRegistryOwner     = "clone"
	cloneRegistrySubsystem = "clone"
	cloneRegistryKind      = "profile_snapshot"

	defaultCloneRegistryReconcileBatch = 32
	maxCloneRegistryReconcileBatch     = 128
)

type mediaRegistryRepository interface {
	RegisterCloneMediaAsset(context.Context, string) error
	RemoveCloneMediaAsset(context.Context, string) error
}

func cloneManagedAssetID(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, cloneAssetRefPrefix) {
		return "", false
	}
	assetID := strings.TrimSpace(strings.TrimPrefix(ref, cloneAssetRefPrefix))
	return assetID, assetID != ""
}

func cloneMediaAssetRegistration(assetID string) mediaregistry.AssetRegistration {
	return mediaregistry.AssetRegistration{
		AssetID:   strings.TrimSpace(assetID),
		Producer:  cloneRegistryProducer,
		Owner:     cloneRegistryOwner,
		Lifecycle: mediaregistry.LifecyclePersistent,
	}
}

func cloneMediaReference(assetID string, ownerID int64) mediaregistry.Reference {
	return mediaregistry.Reference{
		AssetID:   strings.TrimSpace(assetID),
		Subsystem: cloneRegistrySubsystem,
		Kind:      cloneRegistryKind,
		Key:       strconv.FormatInt(ownerID, 10),
	}
}

func normalizeCloneRegistryLimit(limit int) int {
	if limit <= 0 {
		return defaultCloneRegistryReconcileBatch
	}
	return min(limit, maxCloneRegistryReconcileBatch)
}

// ReconcileMediaRegistry backfills durable managed clone snapshots into the
// global registry without touching physical storage. Legacy filesystem paths
// are deliberately ignored because they do not identify a managed asset.
func ReconcileMediaRegistry(ctx context.Context, db *database.DB, limit int) (int, error) {
	if db == nil {
		return 0, mediaregistry.ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ready, err := mediaregistry.SchemaReady(ctx, db)
	if err != nil {
		return 0, err
	}
	if !ready {
		return 0, nil
	}
	limit = normalizeCloneRegistryLimit(limit)

	rows, err := db.QueryContext(ctx, `
        SELECT c.owner_id, TRIM(substr(c.original_photo_path, ?))
        FROM clone_state c
        WHERE c.original_photo_path LIKE ?
          AND TRIM(substr(c.original_photo_path, ?)) <> ''
          AND (
            NOT EXISTS (
                SELECT 1 FROM media_assets m
                WHERE m.asset_id = TRIM(substr(c.original_photo_path, ?))
                  AND m.producer = ?
                  AND m.owner = ?
                  AND m.lifecycle = ?
            )
            OR NOT EXISTS (
                SELECT 1 FROM media_asset_references mr
                WHERE mr.asset_id = TRIM(substr(c.original_photo_path, ?))
                  AND mr.subsystem = ?
                  AND mr.reference_kind = ?
                  AND mr.reference_key = CAST(c.owner_id AS TEXT)
            )
          )
        ORDER BY c.owner_id ASC
        LIMIT ?
    `,
		len(cloneAssetRefPrefix)+1,
		cloneAssetRefPrefix+"%",
		len(cloneAssetRefPrefix)+1,
		len(cloneAssetRefPrefix)+1,
		cloneRegistryProducer,
		cloneRegistryOwner,
		mediaregistry.LifecyclePersistent,
		len(cloneAssetRefPrefix)+1,
		cloneRegistrySubsystem,
		cloneRegistryKind,
		limit,
	)
	if err != nil {
		return 0, fmt.Errorf("clone: list media registry backfill: %w", err)
	}
	defer rows.Close()

	type candidate struct {
		ownerID int64
		assetID string
	}
	candidates := make([]candidate, 0, limit)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.ownerID, &item.assetID); err != nil {
			return 0, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, item := range candidates {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return 0, fmt.Errorf("clone: begin media registry backfill: %w", err)
		}
		if err := mediaregistry.RegisterAssetWithExecutor(
			ctx,
			tx,
			cloneMediaAssetRegistration(item.assetID),
			cloneMediaReference(item.assetID, item.ownerID),
		); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("clone: backfill media registry asset %q: %w", item.assetID, err)
		}
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("clone: commit media registry backfill asset %q: %w", item.assetID, err)
		}
	}
	return len(candidates), nil
}
