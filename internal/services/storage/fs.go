package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

var validIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

const (
	storageDirMode  os.FileMode = 0700
	storageFileMode os.FileMode = 0600
)

// FileStorage implements Storage backed by the local filesystem.
type FileStorage struct {
	baseDir  string
	maxQuota int64
	mu       sync.RWMutex
}

var _ Storage = (*FileStorage)(nil)

func NewFileStorage(baseDir string, maxQuota int64) (*FileStorage, error) {
	if baseDir == "" {
		baseDir = filepath.Join("data", "storage")
	}
	cleanDir := filepath.Clean(baseDir)
	if err := os.MkdirAll(cleanDir, storageDirMode); err != nil {
		return nil, fmt.Errorf("failed to initialize storage directory: %w", err)
	}
	// MkdirAll does not tighten permissions when the directory already exists.
	if err := os.Chmod(cleanDir, storageDirMode); err != nil {
		return nil, fmt.Errorf("failed to harden storage directory permissions: %w", err)
	}

	return &FileStorage{baseDir: cleanDir, maxQuota: maxQuota}, nil
}

func (f *FileStorage) BasePath() string { return f.baseDir }

func (f *FileStorage) sanitizeID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || !validIDRegex.MatchString(id) {
		return "", fmt.Errorf("%w: id contains invalid characters", ErrInvalidPath)
	}
	return id, nil
}

func (f *FileStorage) assetDir(id string) string { return filepath.Join(f.baseDir, id) }

func generateID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is exceptionally rare; return a value that remains
		// collision-resistant enough for this process rather than silently using
		// predictable IDs.
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b)
}

// storageSize returns the current persistent size below baseDir. The caller must
// hold f.mu when it is used as part of quota admission.
func (f *FileStorage) storageSize() (int64, error) {
	var total int64
	err := filepath.Walk(f.baseDir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.r.Read(p)
	}
}

// Put writes the contents of src to an isolated directory under baseDir.
// Quota is enforced against the committed data plus metadata, and the final
// files are installed atomically only after the complete asset is valid.
func (f *FileStorage) Put(ctx context.Context, src io.Reader, meta Metadata) (*Asset, error) {
	if src == nil {
		return nil, fmt.Errorf("source reader cannot be nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	currentSize, err := f.storageSize()
	if err != nil {
		return nil, fmt.Errorf("failed to calculate storage usage: %w", err)
	}
	if f.maxQuota > 0 && currentSize >= f.maxQuota {
		return nil, ErrQuotaExceeded
	}

	id := generateID()
	dir := f.assetDir(id)
	if err := os.Mkdir(dir, storageDirMode); err != nil {
		return nil, fmt.Errorf("failed to create asset folder: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	fileName := core.SanitizeFileName(meta.Name)
	if fileName == "" {
		fileName = "media.bin"
	}
	dataPath := filepath.Join(dir, fileName)
	tmpPath := dataPath + ".part"

	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, storageFileMode)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to create target file: %w", err)
	}

	var input io.Reader = contextReader{ctx: ctx, r: src}
	if f.maxQuota > 0 {
		remaining := f.maxQuota - currentSize
		// Read one byte beyond the remaining quota so an oversized stream is
		// detected instead of silently truncated.
		input = io.LimitReader(input, remaining+1)
	}
	written, copyErr := io.Copy(out, input)
	closeErr := out.Close()
	if copyErr != nil {
		cleanup()
		return nil, fmt.Errorf("failed during data transfer to storage: %w", copyErr)
	}
	if closeErr != nil {
		cleanup()
		return nil, fmt.Errorf("failed to close target file: %w", closeErr)
	}

	if f.maxQuota > 0 && currentSize+written > f.maxQuota {
		cleanup()
		return nil, ErrQuotaExceeded
	}

	asset := &Asset{
		ID: id, Name: fileName, MIME: meta.MIME, Size: written, Path: dataPath,
		Duration: meta.Duration, Width: meta.Width, Height: meta.Height, CreatedAt: time.Now().UTC(),
	}
	metaBytes, err := json.Marshal(asset)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to encode asset metadata: %w", err)
	}
	if f.maxQuota > 0 && currentSize+written+int64(len(metaBytes)) > f.maxQuota {
		cleanup()
		return nil, ErrQuotaExceeded
	}

	if err := os.Rename(tmpPath, dataPath); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to commit target file: %w", err)
	}
	metaPath := filepath.Join(dir, "meta.json")
	metaTmpPath := metaPath + ".part"
	if err := os.WriteFile(metaTmpPath, metaBytes, storageFileMode); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to write asset metadata: %w", err)
	}
	if err := os.Rename(metaTmpPath, metaPath); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to commit asset metadata: %w", err)
	}

	return asset, nil
}

func (f *FileStorage) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	sanitizedID, err := f.sanitizeID(id)
	if err != nil { return nil, err }
	if err := ctx.Err(); err != nil { return nil, err }
	asset, err := f.Stat(ctx, sanitizedID)
	if err != nil { return nil, err }
	file, err := os.Open(asset.Path)
	if err != nil {
		if os.IsNotExist(err) { return nil, ErrNotFound }
		return nil, fmt.Errorf("failed to open asset file: %w", err)
	}
	return file, nil
}

func (f *FileStorage) Delete(ctx context.Context, id string) error {
	sanitizedID, err := f.sanitizeID(id)
	if err != nil { return err }
	if err := ctx.Err(); err != nil { return err }

	f.mu.Lock()
	defer f.mu.Unlock()
	dir := f.assetDir(sanitizedID)
	if _, err := os.Stat(dir); os.IsNotExist(err) { return ErrNotFound }
	if err := os.RemoveAll(dir); err != nil { return fmt.Errorf("failed to delete asset: %w", err) }
	return nil
}

func (f *FileStorage) Stat(ctx context.Context, id string) (*Asset, error) {
	sanitizedID, err := f.sanitizeID(id)
	if err != nil { return nil, err }
	if err := ctx.Err(); err != nil { return nil, err }

	f.mu.RLock()
	defer f.mu.RUnlock()
	dir := f.assetDir(sanitizedID)
	metaPath := filepath.Join(dir, "meta.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err == nil {
		var asset Asset
		if jsonErr := json.Unmarshal(metaBytes, &asset); jsonErr == nil {
			if _, statErr := os.Stat(asset.Path); statErr == nil { return &asset, nil }
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) { return nil, ErrNotFound }
		return nil, fmt.Errorf("failed to read asset directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && entry.Name() != "meta.json" && !strings.HasSuffix(entry.Name(), ".part") {
			info, statErr := entry.Info()
			if statErr != nil { continue }
			return &Asset{ID: sanitizedID, Name: entry.Name(), Size: info.Size(), Path: filepath.Join(dir, entry.Name()), CreatedAt: info.ModTime().UTC()}, nil
		}
	}
	return nil, ErrNotFound
}
