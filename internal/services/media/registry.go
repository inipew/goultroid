package media

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/storage"
)

const (
	mediaRegistryProducer = "media.ffmpeg"
	mediaRegistryOwner    = "media"
)

func transientAssetRegistration(assetID string) mediaregistry.AssetRegistration {
	return mediaregistry.AssetRegistration{
		AssetID:   strings.TrimSpace(assetID),
		Producer:  mediaRegistryProducer,
		Owner:     mediaRegistryOwner,
		Lifecycle: mediaregistry.LifecycleTransient,
	}
}

func transientReclamationRequest(assetID, reason string) mediaregistry.ReclamationRequest {
	return mediaregistry.ReclamationRequest{
		AssetID:   strings.TrimSpace(assetID),
		Owner:     mediaRegistryOwner,
		Lifecycle: mediaregistry.LifecycleTransient,
		Reason:    reason,
		Grace:     mediaregistry.DefaultReclamationGrace,
	}
}

// registerTransientAsset records a newly created FFmpeg output and installs a
// prepared crash-recovery intent. A normal consumer explicitly activates the
// intent after use; a durable reference insertion would cancel it; startup can
// recover leftovers from a process that died before cleanup.
func registerTransientAsset(
	ctx context.Context,
	registry *mediaregistry.Registry,
	store storage.Storage,
	asset *storage.Asset,
) error {
	if registry == nil {
		return nil
	}
	if asset == nil || strings.TrimSpace(asset.ID) == "" {
		return mediaregistry.ErrInvalidAsset
	}
	if err := registry.RegisterAsset(ctx, transientAssetRegistration(asset.ID)); err != nil {
		return rollbackTransientRegistration(ctx, store, asset.ID, err)
	}

	reclaimer := mediaregistry.NewReclaimer(registry, store)
	if err := reclaimer.PrepareReclamation(ctx, transientReclamationRequest(asset.ID, "media transient output guard")); err != nil {
		if store == nil {
			return errors.Join(err, errors.New("media: storage unavailable for transient preparation rollback"))
		}
		cleanupCtx, cancel := transientCleanupContext(ctx)
		defer cancel()
		deleteErr := store.Delete(cleanupCtx, asset.ID)
		if deleteErr != nil && !errors.Is(deleteErr, storage.ErrNotFound) {
			// Keep ownership metadata when bytes remain. Startup's explicit
			// media/transient policy can rediscover and reclaim the asset.
			return errors.Join(err, fmt.Errorf("media: rollback prepared transient asset %q: %w", asset.ID, deleteErr))
		}
		if metadataErr := registry.RemoveOwnedAsset(cleanupCtx, asset.ID, mediaRegistryOwner); metadataErr != nil {
			return errors.Join(err, fmt.Errorf("media: rollback transient ownership %q: %w", asset.ID, metadataErr))
		}
		return err
	}
	return nil
}

func rollbackTransientRegistration(ctx context.Context, store storage.Storage, assetID string, cause error) error {
	if store == nil {
		return errors.Join(cause, errors.New("media: storage unavailable for transient registration rollback"))
	}
	cleanupCtx, cancel := transientCleanupContext(ctx)
	deleteErr := store.Delete(cleanupCtx, assetID)
	cancel()
	if deleteErr != nil && !errors.Is(deleteErr, storage.ErrNotFound) {
		return errors.Join(cause, fmt.Errorf("media: rollback unregistered transient asset %q: %w", assetID, deleteErr))
	}
	return cause
}

func transientCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
}
