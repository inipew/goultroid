package filters

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/savedresponse"
)

func TestSQLiteRepositoryDualWritesMediaRegistry(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, mediaregistry.MigrationProvider{}, Module); err != nil {
		t.Fatal(err)
	}

	repo := NewSQLiteRepository(db)
	registry := mediaregistry.New(db)
	first := savedresponse.NewPlainText("first")
	first.Media = &savedresponse.MediaRef{AssetID: "filter-asset-a", MediaType: "photo", Name: "a.jpg", MIMEType: "image/jpeg"}
	if err := repo.SaveFilter(ctx, 84, "PING", first); err != nil {
		t.Fatal(err)
	}
	assertFilterRegistryReferenceCount(t, registry, "filter-asset-a", 1)

	second := savedresponse.NewPlainText("second")
	second.Media = &savedresponse.MediaRef{AssetID: "filter-asset-b", MediaType: "photo", Name: "b.jpg", MIMEType: "image/jpeg"}
	if err := repo.SaveFilter(ctx, 84, "PING", second); err != nil {
		t.Fatal(err)
	}
	assertFilterRegistryReferenceCount(t, registry, "filter-asset-a", 0)
	assertFilterRegistryReferenceCount(t, registry, "filter-asset-b", 1)

	if err := repo.DeleteFilter(ctx, 84, "PING"); err != nil {
		t.Fatal(err)
	}
	assertFilterRegistryReferenceCount(t, registry, "filter-asset-b", 0)
}

func TestSQLiteRepositoryRollsBackOnMediaRegistryOwnershipConflict(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, mediaregistry.MigrationProvider{}, Module); err != nil {
		t.Fatal(err)
	}

	if err := mediaregistry.New(db).RegisterAsset(ctx, mediaregistry.AssetRegistration{
		AssetID: "filter-collision", Producer: "other.producer", Owner: "other-owner", Lifecycle: mediaregistry.LifecycleRetained,
	}); err != nil {
		t.Fatal(err)
	}

	repo := NewSQLiteRepository(db)
	response := savedresponse.NewPlainText("conflicting")
	response.Media = &savedresponse.MediaRef{AssetID: "filter-collision", MediaType: "photo", Name: "conflict.jpg", MIMEType: "image/jpeg"}
	err = repo.SaveFilter(ctx, 99, "CONFLICT", response)
	if !errors.Is(err, mediaregistry.ErrOwnershipConflict) {
		t.Fatalf("expected ownership conflict, got %v", err)
	}
	row, err := repo.GetFilter(ctx, 99, "CONFLICT")
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("domain row committed despite registry conflict: %+v", row)
	}
}

func assertFilterRegistryReferenceCount(t *testing.T, registry *mediaregistry.Registry, assetID string, want int) {
	t.Helper()
	count, err := registry.ReferenceCount(context.Background(), assetID)
	if err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("reference count for %s=%d, want %d", assetID, count, want)
	}
}
