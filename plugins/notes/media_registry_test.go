package notes

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
	first.Media = &savedresponse.MediaRef{AssetID: "note-asset-a", MediaType: "photo", Name: "a.jpg", MIMEType: "image/jpeg"}
	if err := repo.SaveNote(ctx, 42, "welcome", first); err != nil {
		t.Fatal(err)
	}
	assertNoteRegistryReferenceCount(t, registry, "note-asset-a", 1)

	second := savedresponse.NewPlainText("second")
	second.Media = &savedresponse.MediaRef{AssetID: "note-asset-b", MediaType: "photo", Name: "b.jpg", MIMEType: "image/jpeg"}
	if err := repo.SaveNote(ctx, 42, "welcome", second); err != nil {
		t.Fatal(err)
	}
	assertNoteRegistryReferenceCount(t, registry, "note-asset-a", 0)
	assertNoteRegistryReferenceCount(t, registry, "note-asset-b", 1)

	if err := repo.DeleteNote(ctx, 42, "welcome"); err != nil {
		t.Fatal(err)
	}
	assertNoteRegistryReferenceCount(t, registry, "note-asset-b", 0)
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
		AssetID: "note-collision", Producer: "other.producer", Owner: "other-owner", Lifecycle: mediaregistry.LifecycleRetained,
	}); err != nil {
		t.Fatal(err)
	}

	repo := NewSQLiteRepository(db)
	response := savedresponse.NewPlainText("conflicting")
	response.Media = &savedresponse.MediaRef{AssetID: "note-collision", MediaType: "photo", Name: "conflict.jpg", MIMEType: "image/jpeg"}
	err = repo.SaveNote(ctx, 99, "conflict", response)
	if !errors.Is(err, mediaregistry.ErrOwnershipConflict) {
		t.Fatalf("expected ownership conflict, got %v", err)
	}
	row, err := repo.GetNote(ctx, 99, "conflict")
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("domain row committed despite registry conflict: %+v", row)
	}
}

func assertNoteRegistryReferenceCount(t *testing.T, registry *mediaregistry.Registry, assetID string, want int) {
	t.Helper()
	count, err := registry.ReferenceCount(context.Background(), assetID)
	if err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("reference count for %s=%d, want %d", assetID, count, want)
	}
}
