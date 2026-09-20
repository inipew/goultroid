package media

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/storage"
)

func newMediaRegistryTest(t *testing.T) (*mediaregistry.Registry, *database.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, mediaregistry.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	return mediaregistry.New(db), db
}

func putMediaRegistryTestAsset(t *testing.T, store storage.Storage, name string) *storage.Asset {
	t.Helper()
	asset, err := store.Put(context.Background(), strings.NewReader("payload-"+name), storage.Metadata{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

func TestTransientAssetRegistrationAndCleanup(t *testing.T) {
	ctx := context.Background()
	registry, _ := newMediaRegistryTest(t)
	store := storage.NewMemoryStorage()
	asset := putMediaRegistryTestAsset(t, store, "converted.mp4")

	if err := registerTransientAsset(ctx, registry, store, asset); err != nil {
		t.Fatal(err)
	}
	record, err := registry.Asset(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Producer != mediaRegistryProducer || record.Owner != mediaRegistryOwner || record.Lifecycle != mediaregistry.LifecycleTransient {
		t.Fatalf("unexpected transient ownership metadata: %+v", record)
	}
	refs, err := registry.ReferenceCount(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refs != 0 {
		t.Fatalf("transient asset reference count=%d, want 0", refs)
	}

	svc := NewService(nil, store, nil, registry)
	if err := svc.DeleteTransientAsset(ctx, asset); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(ctx, asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("transient physical asset survived cleanup: %v", err)
	}
	if _, err := registry.Asset(ctx, asset.ID); !errors.Is(err, mediaregistry.ErrAssetNotRegistered) {
		t.Fatalf("transient ownership metadata survived cleanup: %v", err)
	}
}

func TestTransientRegistrationConflictRollsBackPhysicalAsset(t *testing.T) {
	ctx := context.Background()
	registry, _ := newMediaRegistryTest(t)
	store := storage.NewMemoryStorage()
	asset := putMediaRegistryTestAsset(t, store, "collision.mp4")
	if err := registry.RegisterAsset(ctx, mediaregistry.AssetRegistration{
		AssetID: asset.ID, Producer: "other", Owner: "other", Lifecycle: mediaregistry.LifecycleRetained,
	}); err != nil {
		t.Fatal(err)
	}

	err := registerTransientAsset(ctx, registry, store, asset)
	if !errors.Is(err, mediaregistry.ErrOwnershipConflict) {
		t.Fatalf("registration error=%v, want ownership conflict", err)
	}
	if _, err := store.Stat(ctx, asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("conflicting transient asset was not rolled back: %v", err)
	}
	record, err := registry.Asset(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Owner != "other" {
		t.Fatalf("ownership conflict mutated existing owner: %+v", record)
	}
}
