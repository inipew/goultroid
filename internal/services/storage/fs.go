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

// FileStorage implements Storage backed by the local filesystem.
type FileStorage struct {
	baseDir  string
	maxQuota int64
	mu       sync.RWMutex
}

// Ensure FileStorage implements Storage.
var _ Storage = (*FileStorage)(nil)

// NewFileStorage creates a new FileStorage rooted at baseDir with an optional quota (in bytes).
func NewFileStorage(baseDir string, maxQuota int64) (*FileStorage, error) {
	if baseDir == "" {
		baseDir = filepath.Join("data", "storage")
	}
	cleanDir := filepath.Clean(baseDir)
	if err := os.MkdirAll(cleanDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to initialize storage directory: %w", err)
	}

	return &FileStorage{
		baseDir:  cleanDir,
		maxQuota: maxQuota,
	}, nil
}

// BasePath returns the storage root directory.
func (f *FileStorage) BasePath() string {
	return f.baseDir
}

func (f *FileStorage) sanitizeID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || !validIDRegex.MatchString(id) {
		return "", fmt.Errorf("%w: id contains invalid characters", ErrInvalidPath)
	}
	return id, nil
}

func (f *FileStorage) assetDir(id string) string {
	return filepath.Join(f.baseDir, id)
}

func generateID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Put writes the contents of src to an isolated directory under baseDir.
func (f *FileStorage) Put(ctx context.Context, src io.Reader, meta Metadata) (*Asset, error) {
	if src == nil {
		return nil, fmt.Errorf("source reader cannot be nil")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Check directory quota if configured
	if f.maxQuota > 0 {
		var currentSize int64
		_ = filepath.Walk(f.baseDir, func(_ string, info os.FileInfo, err error) error {
			if err == nil && info != nil && !info.IsDir() {
				currentSize += info.Size()
			}
			return nil
		})
		if currentSize >= f.maxQuota {
			return nil, ErrQuotaExceeded
		}
	}

	id := generateID()
	dir := f.assetDir(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create asset folder: %w", err)
	}

	fileName := core.SanitizeFileName(meta.Name)
	if fileName == "" {
		fileName = "media.bin"
	}

	dataPath := filepath.Join(dir, fileName)
	out, err := os.OpenFile(dataPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("failed to create target file: %w", err)
	}

	written, err := io.Copy(out, src)
	_ = out.Close()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("failed during data transfer to storage: %w", err)
	}

	asset := &Asset{
		ID:        id,
		Name:      fileName,
		MIME:      meta.MIME,
		Size:      written,
		Path:      dataPath,
		Duration:  meta.Duration,
		Width:     meta.Width,
		Height:    meta.Height,
		CreatedAt: time.Now(),
	}

	// Persist sidecar metadata
	metaPath := filepath.Join(dir, "meta.json")
	if metaBytes, err := json.Marshal(asset); err == nil {
		_ = os.WriteFile(metaPath, metaBytes, 0644)
	}

	return asset, nil
}

// Open retrieves the reader for an asset file.
func (f *FileStorage) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	sanitizedID, err := f.sanitizeID(id)
	if err != nil {
		return nil, err
	}

	asset, err := f.Stat(ctx, sanitizedID)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(asset.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to open asset file: %w", err)
	}

	return file, nil
}

// Delete removes the asset folder and all stored files.
func (f *FileStorage) Delete(ctx context.Context, id string) error {
	sanitizedID, err := f.sanitizeID(id)
	if err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	dir := f.assetDir(sanitizedID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return ErrNotFound
	}

	return os.RemoveAll(dir)
}

// Stat retrieves asset metadata without reading file content.
func (f *FileStorage) Stat(ctx context.Context, id string) (*Asset, error) {
	sanitizedID, err := f.sanitizeID(id)
	if err != nil {
		return nil, err
	}

	f.mu.RLock()
	defer f.mu.RUnlock()

	dir := f.assetDir(sanitizedID)
	metaPath := filepath.Join(dir, "meta.json")

	metaBytes, err := os.ReadFile(metaPath)
	if err == nil {
		var asset Asset
		if jsonErr := json.Unmarshal(metaBytes, &asset); jsonErr == nil {
			if _, statErr := os.Stat(asset.Path); statErr == nil {
				return &asset, nil
			}
		}
	}

	// Fallback to directory scan if meta.json missing
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to read asset directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() && entry.Name() != "meta.json" {
			info, statErr := entry.Info()
			if statErr != nil {
				continue
			}
			return &Asset{
				ID:        sanitizedID,
				Name:      entry.Name(),
				Size:      info.Size(),
				Path:      filepath.Join(dir, entry.Name()),
				CreatedAt: info.ModTime(),
			}, nil
		}
	}

	return nil, ErrNotFound
}
