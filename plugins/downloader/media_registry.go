package downloader

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
	downloaderRegistryOwner    = "downloader"
	downloaderTelegramProducer = "downloader.telegram"
)

func downloaderProviderProducer(providerName string) string {
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	if providerName == "" {
		providerName = "unknown"
	}
	return "downloader." + providerName
}

func retainedAssetRegistration(assetID, producer string) mediaregistry.AssetRegistration {
	return mediaregistry.AssetRegistration{
		AssetID:   strings.TrimSpace(assetID),
		Producer:  strings.TrimSpace(producer),
		Owner:     downloaderRegistryOwner,
		Lifecycle: mediaregistry.LifecycleRetained,
	}
}

// registerRetainedAsset records a user-retained download. Retained outputs have
// no durable domain reference by design; lifecycle metadata is the protection
// against treating zero references as orphan evidence.
func (p *Plugin) registerRetainedAsset(
	ctx context.Context,
	store storage.Storage,
	asset *storage.Asset,
	producer string,
) error {
	if p == nil || p.mediaRegistry == nil {
		return nil
	}
	if asset == nil || strings.TrimSpace(asset.ID) == "" || strings.TrimSpace(producer) == "" {
		return mediaregistry.ErrInvalidAsset
	}
	if err := p.mediaRegistry.RegisterAsset(ctx, retainedAssetRegistration(asset.ID, producer)); err != nil {
		if store == nil {
			return errors.Join(err, errors.New("downloader: storage unavailable for ownership rollback"))
		}
		if ctx == nil {
			ctx = context.Background()
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		deleteErr := store.Delete(cleanupCtx, asset.ID)
		cancel()
		if deleteErr != nil && !errors.Is(deleteErr, storage.ErrNotFound) {
			return errors.Join(err, fmt.Errorf("downloader: rollback unregistered retained asset %q: %w", asset.ID, deleteErr))
		}
		return err
	}
	return nil
}
