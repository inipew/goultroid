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
	content := []byte("hello world storage media content")
	meta := storage.Metadata{Name: "test_audio.mp3", MIME: "audio/mp3", Duration: 10 * time.Second}

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

	statAsset, err := store.Stat(ctx, asset.ID)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if statAsset.ID != asset.ID || statAsset.Size != asset.Size {
		t.Errorf("Stat mismatch: %+v vs %v", statAsset, asset)
	}

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

	if err := store.Delete(ctx, asset.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := store.Stat(ctx, asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
	if _, err := store.Open(ctx, asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound on Open after delete, got %v", err)
	}
}

func TestMemoryStorage(t *testing.T) {
	testStorageSuite(t, storage.NewMemoryStorage())
}

func TestFileStorage(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "goultroid-store-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := storage.NewFileStorage(tempDir, 10*1024*1024)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}
	testStorageSuite(t, store)

	info, err := os.Stat(tempDir)
	if err != nil {
		t.Fatalf("stat storage root: %v", err)
	}
	if got := info.Mode().Perm(); got != 0700 {
		t.Errorf("expected storage root mode 0700, got %o", got)
	}

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

func TestFileStorageHardQuotaIncludesMetadata(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "goultroid-store-quota-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := storage.NewFileStorage(tempDir, 512)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}
	ctx := context.Background()

	_, err = store.Put(ctx, strings.NewReader(strings.Repeat("A", 600)), storage.Metadata{Name: "large.txt"})
	if !errors.Is(err, storage.ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded for oversized stream, got %v", err)
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("read storage root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no committed asset after quota rejection, found %d entries", len(entries))
	}

	asset, err := store.Put(ctx, strings.NewReader(strings.Repeat("B", 20)), storage.Metadata{Name: "small.txt"})
	if err != nil {
		t.Fatalf("small Put failed: %v", err)
	}
	if asset.Size != 20 {
		t.Fatalf("expected 20 bytes, got %d", asset.Size)
	}

	_, err = store.Put(ctx, strings.NewReader(strings.Repeat("C", 400)), storage.Metadata{Name: "next.txt"})
	if !errors.Is(err, storage.ErrQuotaExceeded) {
		t.Errorf("expected ErrQuotaExceeded after quota consumption, got %v", err)
	}
}

func TestFileStorageContextCancellation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "goultroid-store-cancel-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	store, err := storage.NewFileStorage(tempDir, 1024*1024)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.Put(ctx, strings.NewReader("payload"), storage.Metadata{Name: "cancelled.txt"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestFileStorageCleansPartialFilesOnStartup(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "goultroid-store-recovery-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	assetDir := filepath.Join(tempDir, "deadbeef")
	if err := os.MkdirAll(assetDir, 0700); err != nil {
		t.Fatalf("mkdir asset dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "payload.part"), []byte("partial"), 0600); err != nil {
		t.Fatalf("write partial: %v", err)
	}

	if _, err := storage.NewFileStorage(tempDir, 1024); err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(assetDir, "payload.part")); !os.IsNotExist(err) {
		t.Fatalf("expected stale .part file to be removed, stat error=%v", err)
	}
}

func TestFileStorageBasePath(t *testing.T) {
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
