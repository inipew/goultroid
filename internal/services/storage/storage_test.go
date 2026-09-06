package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/services/storage"
)

func testStorageSuite(t *testing.T, store storage.Storage) {
	ctx := context.Background()

	// 1. Put asset
	content := []byte("hello world storage media content")
	meta := storage.Metadata{
		Name:     "test_audio.mp3",
		MIME:     "audio/mp3",
		Duration: 10 * time.Second,
		Width:    0,
		Height:   0,
	}

	asset, err := store.Put(ctx, bytes.NewReader(content), meta)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if asset.ID == "" {
		t.Errorf("expected non-empty ID")
	}
	if asset.Size != int64(len(content)) {
		t.Errorf("expected size %d, got %d", len(content), asset.Size)
	}
	if asset.Name != "test_audio.mp3" {
		t.Errorf("expected name test_audio.mp3, got %s", asset.Name)
	}

	// 2. Stat asset
	statAsset, err := store.Stat(ctx, asset.ID)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if statAsset.ID != asset.ID || statAsset.Size != asset.Size {
		t.Errorf("Stat mismatch: %+v vs %+v", statAsset, asset)
	}

	// 3. Open asset
	rc, err := store.Open(ctx, asset.ID)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	readBack, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(readBack, content) {
		t.Errorf("content mismatch: got %s, expected %s", string(readBack), string(content))
	}

	// 4. Delete asset
	if err := store.Delete(ctx, asset.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// 5. Verify deleted
	if _, err := store.Stat(ctx, asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
	if _, err := store.Open(ctx, asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound on Open after delete, got %v", err)
	}
}

func TestMemoryStorage(t *testing.T) {
	store := storage.NewMemoryStorage()
	testStorageSuite(t, store)
}

func TestFileStorage(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "goultroid-store-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := storage.NewFileStorage(tempDir, 10*1024*1024) // 10MB quota
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}

	testStorageSuite(t, store)

	// Test path traversal protection
	ctx := context.Background()
	badIDs := []string{"../etc", "foo/bar", "../../secret", ""}
	for _, badID := range badIDs {
		if _, err := store.Stat(ctx, badID); err == nil {
			t.Errorf("expected error for bad ID %q, got nil", badID)
		}
		if _, err := store.Open(ctx, badID); err == nil {
			t.Errorf("expected error for bad ID %q, got nil", badID)
		}
		if err := store.Delete(ctx, badID); err == nil {
			t.Errorf("expected error for bad ID %q, got nil", badID)
		}
	}
}

func TestFileStorage_QuotaExceeded(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "goultroid-store-quota-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set tiny 100 byte quota
	store, err := storage.NewFileStorage(tempDir, 100)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}

	ctx := context.Background()
	data := strings.Repeat("A", 120)
	_, err = store.Put(ctx, strings.NewReader(data), storage.Metadata{Name: "large.txt"})
	// 1st put will write 120 bytes (exceeding 100 byte quota for future puts)
	if err != nil {
		t.Logf("first put returned %v", err)
	}

	// 2nd put should definitely fail quota
	_, err = store.Put(ctx, strings.NewReader("another file"), storage.Metadata{Name: "next.txt"})
	if !errors.Is(err, storage.ErrQuotaExceeded) {
		t.Errorf("expected ErrQuotaExceeded, got %v", err)
	}
}

func TestFileStorage_BasePath(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "goultroid-store-path-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := storage.NewFileStorage(tempDir, 0)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}

	if store.BasePath() != filepath.Clean(tempDir) {
		t.Errorf("expected base path %s, got %s", filepath.Clean(tempDir), store.BasePath())
	}
}
