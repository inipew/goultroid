package mediaregistry_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/storage"
)

func TestObserveStorageClassifiesWithoutMutation(t *testing.T) {
	registry, _ := newRegistry(t)
	root := t.TempDir()
	store, err := storage.NewFileStorage(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	trackedRef, err := store.Put(ctx, bytes.NewReader([]byte("tracked-ref")), storage.Metadata{Name: "a.bin"})
	if err != nil {
		t.Fatal(err)
	}
	trackedNoRef, err := store.Put(ctx, bytes.NewReader([]byte("tracked-no-ref")), storage.Metadata{Name: "b.bin"})
	if err != nil {
		t.Fatal(err)
	}
	untrackedRef, err := store.Put(ctx, bytes.NewReader([]byte("untracked-ref")), storage.Metadata{Name: "c.bin"})
	if err != nil {
		t.Fatal(err)
	}
	untrackedNoRef, err := store.Put(ctx, bytes.NewReader([]byte("legacy")), storage.Metadata{Name: "d.bin"})
	if err != nil {
		t.Fatal(err)
	}

	if err := registry.RegisterAsset(ctx, mediaregistry.AssetRegistration{
		AssetID: trackedRef.ID, Producer: "savedresponse.capture", Owner: "savedresponse", Lifecycle: mediaregistry.LifecyclePersistent,
	}, mediaregistry.Reference{AssetID: trackedRef.ID, Subsystem: "notes", Kind: "note", Key: "1:a"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterAsset(ctx, mediaregistry.AssetRegistration{
		AssetID: trackedNoRef.ID, Producer: "downloader.http", Owner: "downloader", Lifecycle: mediaregistry.LifecycleRetained,
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.UpsertReference(ctx, mediaregistry.Reference{
		AssetID: untrackedRef.ID, Subsystem: "clone", Kind: "profile_snapshot", Key: "42",
	}); err != nil {
		t.Fatal(err)
	}

	legacyRoot := filepath.Join(root, "manual-download.bin")
	if err := os.WriteFile(legacyRoot, []byte("manual"), 0o600); err != nil {
		t.Fatal(err)
	}
	malformedID := "deadbeef"
	if err := os.Mkdir(filepath.Join(root, malformedID), 0o700); err != nil {
		t.Fatal(err)
	}

	classes := map[string]mediaregistry.ObservationClass{}
	cursor := ""
	for {
		page, err := registry.ObserveStorage(ctx, store, storage.EnumerationOptions{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) > 2 {
			t.Fatalf("observer exceeded storage page bound: %d", len(page.Entries))
		}
		for _, entry := range page.Entries {
			classes[entry.Physical.Key] = entry.Class
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	want := map[string]mediaregistry.ObservationClass{
		trackedRef.ID:         mediaregistry.ObservationTrackedReferenced,
		trackedNoRef.ID:       mediaregistry.ObservationTrackedUnreferenced,
		untrackedRef.ID:       mediaregistry.ObservationUntrackedReferenced,
		untrackedNoRef.ID:     mediaregistry.ObservationLegacyUntracked,
		"manual-download.bin": mediaregistry.ObservationLegacyUntracked,
		malformedID:           mediaregistry.ObservationMalformed,
	}
	for key, class := range want {
		if got := classes[key]; got != class {
			t.Fatalf("entry %q class=%q, want %q", key, got, class)
		}
	}

	for _, asset := range []*storage.Asset{trackedRef, trackedNoRef, untrackedRef, untrackedNoRef} {
		if _, err := store.Stat(ctx, asset.ID); err != nil {
			t.Fatalf("observe-only registry mutated asset %q: %v", asset.ID, err)
		}
	}
	if _, err := os.Stat(legacyRoot); err != nil {
		t.Fatalf("observe-only registry mutated unmanaged entry: %v", err)
	}
}
