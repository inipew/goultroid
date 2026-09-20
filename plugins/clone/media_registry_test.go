package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/storage"
)

func newCloneRegistryTestDB(t *testing.T) *database.DB {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(ctx, db, mediaregistry.MigrationProvider{}, Module); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSQLiteRepositoryDualWritesCloneMediaRegistry(t *testing.T) {
	ctx := context.Background()
	db := newCloneRegistryTestDB(t)
	repo := NewSQLiteRepository(db)
	registry := mediaregistry.New(db)

	first := CloneState{OwnerID: 42, OriginalPhoto: "asset:clone-a", Active: true, UpdatedAt: time.Now().UTC()}
	if err := repo.SaveCloneState(ctx, first); err != nil {
		t.Fatal(err)
	}
	assertCloneRegistryAsset(t, registry, "clone-a", 1)

	second := first
	second.OriginalPhoto = "asset:clone-b"
	second.UpdatedAt = time.Now().UTC()
	if err := repo.SaveCloneState(ctx, second); err != nil {
		t.Fatal(err)
	}
	assertCloneRegistryAsset(t, registry, "clone-a", 0)
	assertCloneRegistryAsset(t, registry, "clone-b", 1)

	if err := repo.ClearCloneState(ctx, first.OwnerID); err != nil {
		t.Fatal(err)
	}
	assertCloneRegistryAsset(t, registry, "clone-b", 0)
	if err := repo.RemoveCloneMediaAsset(ctx, "clone-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Asset(ctx, "clone-b"); !errors.Is(err, mediaregistry.ErrAssetNotRegistered) {
		t.Fatalf("cleared snapshot metadata still registered: %v", err)
	}
}

func TestSQLiteRepositoryRollsBackCloneStateOnOwnershipConflict(t *testing.T) {
	ctx := context.Background()
	db := newCloneRegistryTestDB(t)
	registry := mediaregistry.New(db)
	if err := registry.RegisterAsset(ctx, mediaregistry.AssetRegistration{
		AssetID: "collision", Producer: "other", Owner: "other", Lifecycle: mediaregistry.LifecycleRetained,
	}); err != nil {
		t.Fatal(err)
	}

	repo := NewSQLiteRepository(db)
	err := repo.SaveCloneState(ctx, CloneState{
		OwnerID: 7, OriginalPhoto: "asset:collision", Active: true, UpdatedAt: time.Now().UTC(),
	})
	if !errors.Is(err, mediaregistry.ErrOwnershipConflict) {
		t.Fatalf("save error=%v, want ownership conflict", err)
	}
	state, err := repo.GetCloneState(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if state != nil {
		t.Fatalf("clone state committed despite ownership conflict: %+v", state)
	}
}

func TestReconcileCloneMediaRegistryBackfillsManagedSnapshotsBoundedly(t *testing.T) {
	ctx := context.Background()
	db := newCloneRegistryTestDB(t)
	now := time.Now().UTC()
	for ownerID, ref := range map[int64]string{
		1: "asset:legacy-a",
		2: "asset:legacy-b",
		3: "/legacy/unmanaged/photo.jpg",
	} {
		if _, err := db.ExecContext(ctx, `
            INSERT INTO clone_state (
                owner_id, original_first_name, original_last_name, original_bio,
                original_photo_path, cloned_photo, active, updated_at
            ) VALUES (?, '', '', '', ?, 0, 1, ?)
        `, ownerID, ref, now); err != nil {
			t.Fatal(err)
		}
	}

	first, err := ReconcileMediaRegistry(ctx, db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 {
		t.Fatalf("first backfill=%d, want 1", first)
	}
	second, err := ReconcileMediaRegistry(ctx, db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if second != 1 {
		t.Fatalf("second backfill=%d, want 1", second)
	}
	third, err := ReconcileMediaRegistry(ctx, db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if third != 0 {
		t.Fatalf("third backfill=%d, want idempotent 0", third)
	}

	registry := mediaregistry.New(db)
	assertCloneRegistryAsset(t, registry, "legacy-a", 1)
	assertCloneRegistryAsset(t, registry, "legacy-b", 1)
}

func TestCloneSnapshotCreationRegistersAndCleanupRemovesOwnership(t *testing.T) {
	ctx := context.Background()
	db := newCloneRegistryTestDB(t)
	repo := NewSQLiteRepository(db)
	store := storage.NewMemoryStorage()
	p := New(repo, 99, store)

	path := filepath.Join(t.TempDir(), "original.jpg")
	if err := os.WriteFile(path, []byte("snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := p.storeOriginalPhotoSnapshot(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	assetID, ok := cloneManagedAssetID(ref)
	if !ok {
		t.Fatalf("snapshot ref=%q is not managed", ref)
	}
	assertCloneRegistryAsset(t, mediaregistry.New(db), assetID, 0)

	if err := p.cleanupSnapshot(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(ctx, assetID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("snapshot still present after cleanup: %v", err)
	}
	if _, err := mediaregistry.New(db).Asset(ctx, assetID); !errors.Is(err, mediaregistry.ErrAssetNotRegistered) {
		t.Fatalf("snapshot ownership metadata survived cleanup: %v", err)
	}
}

func assertCloneRegistryAsset(t *testing.T, registry *mediaregistry.Registry, assetID string, wantRefs int) {
	t.Helper()
	record, err := registry.Asset(context.Background(), assetID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Producer != cloneRegistryProducer || record.Owner != cloneRegistryOwner || record.Lifecycle != mediaregistry.LifecyclePersistent {
		t.Fatalf("unexpected clone registration for %s: %+v", assetID, record)
	}
	refs, err := registry.ReferenceCount(context.Background(), assetID)
	if err != nil {
		t.Fatal(err)
	}
	if refs != wantRefs {
		t.Fatalf("reference count for %s=%d, want %d", assetID, refs, wantRefs)
	}
}
