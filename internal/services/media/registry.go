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

// registerTransientAsset records a newly created FFmpeg output. If registry
// persistence fails, the fresh physical asset is rolled back so stage-3 never
// creates an untracked managed asset on a failed ownership write.
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
		if store == nil {
			return errors.Join(err, errors.New("media: storage unavailable for transient registration rollback"))
		}
		if ctx == nil {
			ctx = context.Background()
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		deleteErr := store.Delete(cleanupCtx, asset.ID)
		cancel()
		if deleteErr != nil && !errors.Is(deleteErr, storage.ErrNotFound) {
			return errors.Join(err, fmt.Errorf("media: rollback unregistered transient asset %q: %w", asset.ID, deleteErr))
		}
		return err
	}
	return nil
}
