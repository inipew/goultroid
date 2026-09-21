package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/storage"
)

func TestBuiltinStartupReclaimsOnlyPreparedOwnedAssets(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateBuiltinFeatures(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFileStorage(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}

	owned, err := store.Put(ctx, strings.NewReader("transient"), storage.Metadata{Name: "transient.bin"})
	if err != nil {
		t.Fatal(err)
	}
	policyOwned, err := store.Put(ctx, strings.NewReader("old-transient"), storage.Metadata{Name: "old-transient.bin"})
	if err != nil {
		t.Fatal(err)
	}
	retained, err := store.Put(ctx, strings.NewReader("retained"), storage.Metadata{Name: "retained.bin"})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := store.Put(ctx, strings.NewReader("legacy-untracked"), storage.Metadata{Name: "legacy.bin"})
	if err != nil {
		t.Fatal(err)
	}
	registry := mediaregistry.New(db)
	if err := registry.RegisterAsset(ctx, mediaregistry.AssetRegistration{
		AssetID: owned.ID, Producer: "media.ffmpeg", Owner: "media", Lifecycle: mediaregistry.LifecycleTransient,
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterAsset(ctx, mediaregistry.AssetRegistration{
		AssetID: policyOwned.ID, Producer: "media.ffmpeg", Owner: "media", Lifecycle: mediaregistry.LifecycleTransient,
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterAsset(ctx, mediaregistry.AssetRegistration{
		AssetID: retained.ID, Producer: "downloader.http", Owner: "downloader", Lifecycle: mediaregistry.LifecycleRetained,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mediaregistry.NewReclaimer(registry, store).PrepareReclamation(ctx, mediaregistry.ReclamationRequest{
		AssetID: owned.ID, Owner: "media", Lifecycle: mediaregistry.LifecycleTransient,
		Reason: "crash guard", Grace: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := reconcileBuiltinPersistentMedia(ctx, db, store); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(ctx, owned.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("prepared owned asset survived startup reclamation: %v", err)
	}
	if _, err := registry.Asset(ctx, owned.ID); !errors.Is(err, mediaregistry.ErrAssetNotRegistered) {
		t.Fatalf("reclaimed ownership metadata survived: %v", err)
	}
	if _, err := store.Stat(ctx, policyOwned.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("policy-authorized transient survived startup reclamation: %v", err)
	}
	if _, err := store.Stat(ctx, retained.ID); err != nil {
		t.Fatalf("retained asset was touched by startup policy: %v", err)
	}
	if _, err := store.Stat(ctx, legacy.ID); err != nil {
		t.Fatalf("legacy/untracked asset was touched by global reclaimer: %v", err)
	}
}
