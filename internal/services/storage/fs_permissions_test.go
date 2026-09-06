package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/inipew/goultroid/internal/services/storage"
)

func TestFileStoragePermissions(t *testing.T) {
	tempDir := t.TempDir()
	store, err := storage.NewFileStorage(tempDir, 1024*1024)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}

	asset, err := store.Put(context.Background(), &fixedReader{data: []byte("secret")}, storage.Metadata{Name: "secret.bin"})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	rootInfo, err := os.Stat(tempDir)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if got := rootInfo.Mode().Perm(); got != 0700 {
		t.Fatalf("storage root mode = %o, want 700", got)
	}

	dir := filepath.Dir(asset.Path)
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat asset dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Fatalf("asset dir mode = %o, want 700", got)
	}

	fileInfo, err := os.Stat(asset.Path)
	if err != nil {
		t.Fatalf("stat asset: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0600 {
		t.Fatalf("asset file mode = %o, want 600", got)
	}

	metaInfo, err := os.Stat(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("stat metadata: %v", err)
	}
	if got := metaInfo.Mode().Perm(); got != 0600 {
		t.Fatalf("metadata mode = %o, want 600", got)
	}
}

type fixedReader struct{ data []byte }

func (r *fixedReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, os.ErrClosed
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}
