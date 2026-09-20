package storage_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/inipew/goultroid/internal/services/storage"
)

func collectEnumeration(t *testing.T, store storage.Storage, limit int) []storage.EnumerationEntry {
	t.Helper()
	ctx := context.Background()
	cursor := ""
	var entries []storage.EnumerationEntry
	for {
		page, err := store.Enumerate(ctx, storage.EnumerationOptions{Cursor: cursor, Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) > limit {
			t.Fatalf("page exceeded requested limit: got %d want <= %d", len(page.Entries), limit)
		}
		entries = append(entries, page.Entries...)
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatalf("enumeration cursor did not advance: %q", cursor)
		}
		cursor = page.NextCursor
	}
	return entries
}

func testBoundedEnumeration(t *testing.T, store storage.Storage) {
	t.Helper()
	ctx := context.Background()
	want := make(map[string]struct{})
	for i := 0; i < 5; i++ {
		asset, err := store.Put(ctx, bytes.NewReader([]byte{byte('a' + i)}), storage.Metadata{Name: "asset.bin"})
		if err != nil {
			t.Fatal(err)
		}
		want[asset.ID] = struct{}{}
	}

	entries := collectEnumeration(t, store, 2)
	if len(entries) != len(want) {
		t.Fatalf("enumerated %d entries, want %d", len(entries), len(want))
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.State != storage.EnumerationManaged || entry.Asset == nil {
			t.Fatalf("unexpected managed entry: %+v", entry)
		}
		if entry.Asset.ID != entry.Key {
			t.Fatalf("entry key/id mismatch: %+v", entry)
		}
		if _, exists := seen[entry.Key]; exists {
			t.Fatalf("duplicate enumeration entry %q", entry.Key)
		}
		seen[entry.Key] = struct{}{}
	}
	for id := range want {
		if _, exists := seen[id]; !exists {
			t.Fatalf("asset %q was skipped by pagination", id)
		}
	}
}

func TestMemoryStorageBoundedEnumeration(t *testing.T) {
	testBoundedEnumeration(t, storage.NewMemoryStorage())
}

func TestFileStorageBoundedEnumeration(t *testing.T) {
	store, err := storage.NewFileStorage(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	testBoundedEnumeration(t, store)
}

func TestFileStorageEnumerationClassifiesUnmanagedAndMalformedEntries(t *testing.T) {
	root := t.TempDir()
	store, err := storage.NewFileStorage(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := store.Put(context.Background(), bytes.NewReader([]byte("managed")), storage.Metadata{Name: "managed.bin"})
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(root, "legacy-download.bin")
	if err := os.WriteFile(legacyPath, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	malformedID := "deadbeef"
	if err := os.Mkdir(filepath.Join(root, malformedID), 0o700); err != nil {
		t.Fatal(err)
	}

	entries := collectEnumeration(t, store, 2)
	states := make(map[string]storage.EnumerationState, len(entries))
	for _, entry := range entries {
		states[entry.Key] = entry.State
	}
	if states[asset.ID] != storage.EnumerationManaged {
		t.Fatalf("managed asset classified as %q", states[asset.ID])
	}
	if states["legacy-download.bin"] != storage.EnumerationUnmanaged {
		t.Fatalf("legacy root file classified as %q", states["legacy-download.bin"])
	}
	if states[malformedID] != storage.EnumerationMalformed {
		t.Fatalf("empty asset directory classified as %q", states[malformedID])
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("observe-only enumeration mutated legacy file: %v", err)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("observe-only enumeration mutated managed asset: %v", err)
	}
}

func TestStorageEnumerationClampsLimit(t *testing.T) {
	store := storage.NewMemoryStorage()
	for i := 0; i < storage.MaxEnumerationLimit+10; i++ {
		if _, err := store.Put(context.Background(), bytes.NewReader([]byte("x")), storage.Metadata{Name: "x.bin"}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Enumerate(context.Background(), storage.EnumerationOptions{Limit: storage.MaxEnumerationLimit + 1000})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(page.Entries); got != storage.MaxEnumerationLimit {
		t.Fatalf("enumeration returned %d entries, want %d", got, storage.MaxEnumerationLimit)
	}
	if page.NextCursor == "" {
		t.Fatal("expected continuation cursor for clamped page")
	}
}
