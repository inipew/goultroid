package mediaregistry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
)

func newRegistry(t *testing.T) (*mediaregistry.Registry, *database.DB) {
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

func TestMigrationSeparatesAssetsAndReferencesWithoutRefCount(t *testing.T) {
	_, db := newRegistry(t)
	for _, table := range []string{"media_assets", "media_asset_references"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %s missing", table)
		}
	}
	var refCountColumn int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('media_assets') WHERE name = 'ref_count'`).Scan(&refCountColumn); err != nil {
		t.Fatal(err)
	}
	if refCountColumn != 0 {
		t.Fatal("media_assets must not use mutable ref_count as reachability truth")
	}
}

func TestRegisterAssetAndReferences(t *testing.T) {
	registry, _ := newRegistry(t)
	ctx := context.Background()
	reg := mediaregistry.AssetRegistration{
		AssetID: "asset-a", Producer: "savedresponse.capture", Owner: "savedresponse", Lifecycle: mediaregistry.LifecyclePersistent,
	}
	ref := mediaregistry.Reference{AssetID: "asset-a", Subsystem: "notes", Kind: "note", Key: "123:welcome"}
	if err := registry.RegisterAsset(ctx, reg, ref); err != nil {
		t.Fatal(err)
	}
	record, err := registry.Asset(ctx, "asset-a")
	if err != nil {
		t.Fatal(err)
	}
	if record.Producer != reg.Producer || record.Owner != reg.Owner || record.Lifecycle != reg.Lifecycle {
		t.Fatalf("unexpected record: %+v", record)
	}
	count, err := registry.ReferenceCount(ctx, "asset-a")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reference count=%d, want 1", count)
	}
	if err := registry.RemoveReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	count, err = registry.ReferenceCount(ctx, "asset-a")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("reference row survived removal: %d", count)
	}
}

func TestRegisterAssetRejectsOwnershipTakeover(t *testing.T) {
	registry, _ := newRegistry(t)
	ctx := context.Background()
	if err := registry.RegisterAsset(ctx, mediaregistry.AssetRegistration{
		AssetID: "shared", Producer: "clone.snapshot", Owner: "clone", Lifecycle: mediaregistry.LifecyclePersistent,
	}); err != nil {
		t.Fatal(err)
	}
	err := registry.RegisterAsset(ctx, mediaregistry.AssetRegistration{
		AssetID: "shared", Producer: "downloader.http", Owner: "downloader", Lifecycle: mediaregistry.LifecycleRetained,
	})
	if !errors.Is(err, mediaregistry.ErrOwnershipConflict) {
		t.Fatalf("expected ownership conflict, got %v", err)
	}
}

func TestReferenceCanExistBeforeAssetRegistration(t *testing.T) {
	registry, _ := newRegistry(t)
	ctx := context.Background()
	ref := mediaregistry.Reference{AssetID: "legacy-a", Subsystem: "clone", Kind: "profile_snapshot", Key: "42"}
	if err := registry.UpsertReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Asset(ctx, ref.AssetID); !errors.Is(err, mediaregistry.ErrAssetNotRegistered) {
		t.Fatalf("untracked reference unexpectedly registered asset: %v", err)
	}
	count, err := registry.ReferenceCount(ctx, ref.AssetID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reference count=%d, want 1", count)
	}
}

func TestReferenceKeyIsPreservedVerbatim(t *testing.T) {
	registry, db := newRegistry(t)
	ctx := context.Background()
	ref := mediaregistry.Reference{AssetID: "asset-key", Subsystem: "notes", Kind: "note", Key: "  domain key  "}
	if err := registry.UpsertReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	var key string
	if err := db.QueryRowContext(ctx, `
		SELECT reference_key FROM media_asset_references WHERE asset_id = ?
	`, ref.AssetID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != ref.Key {
		t.Fatalf("reference key=%q, want exact %q", key, ref.Key)
	}
}

func TestRegisterAssetValidatesAllReferencesBeforeMutation(t *testing.T) {
	registry, _ := newRegistry(t)
	ctx := context.Background()
	err := registry.RegisterAsset(ctx, mediaregistry.AssetRegistration{
		AssetID: "asset-b", Producer: "test", Owner: "test", Lifecycle: mediaregistry.LifecyclePersistent,
	}, mediaregistry.Reference{AssetID: "other", Subsystem: "test", Kind: "row", Key: "1"})
	if !errors.Is(err, mediaregistry.ErrInvalidReference) {
		t.Fatalf("expected invalid reference, got %v", err)
	}
	if _, err := registry.Asset(ctx, "asset-b"); !errors.Is(err, mediaregistry.ErrAssetNotRegistered) {
		t.Fatalf("failed registration mutated asset registry: %v", err)
	}
}
