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
	usage    int64
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
	if err := os.Chmod(cleanDir, storageDirMode); err != nil {
		return nil, fmt.Errorf("failed to harden storage directory permissions: %w", err)
	}
	if err := cleanupPartialFiles(cleanDir); err != nil {
		return nil, fmt.Errorf("failed to clean partial storage files: %w", err)
	}

	usage, err := storageSizeAt(cleanDir)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate initial storage usage: %w", err)
	}
	return &FileStorage{baseDir: cleanDir, maxQuota: maxQuota, usage: usage}, nil
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

func generateID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate secure asset ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func cleanupPartialFiles(baseDir string) error {
	return filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info == nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".part") {
			return nil
		}
		return os.Remove(path)
	})
}

func storageSizeAt(baseDir string) (int64, error) {
	var total int64
	err := filepath.Walk(baseDir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// storageSize is retained for reconciliation/debugging. Normal Put/Delete paths
// use the cached usage counter so quota admission does not scan the whole tree.
func (f *FileStorage) storageSize() (int64, error) {
	return storageSizeAt(f.baseDir)
}

func (f *FileStorage) reconcileUsage() error {
	usage, err := f.storageSize()
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.usage = usage
	f.mu.Unlock()
	return nil
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

func (f *FileStorage) validateAssetPath(assetDir, path string) error {
	if path == "" {
		return ErrInvalidPath
	}
	rootAbs, err := filepath.Abs(assetDir)
	if err != nil {
		return fmt.Errorf("failed to resolve asset directory: %w", err)
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("failed to resolve asset path: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%w: asset path escapes asset directory", ErrInvalidPath)
	}
	return nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: symlink is not allowed", ErrInvalidPath)
	}
	return nil
}

// Put writes the contents of src to an isolated directory under baseDir.
// Quota is enforced against committed data plus metadata, and final files are
// installed atomically only after the complete asset is valid.
func (f *FileStorage) Put(ctx context.Context, src io.Reader, meta Metadata) (*Asset, error) {
	if src == nil {
		return nil, fmt.Errorf("source reader cannot be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	currentSize := f.usage
	if f.maxQuota > 0 && currentSize >= f.maxQuota {
		return nil, ErrQuotaExceeded
	}

	id, err := generateID()
	if err != nil {
		return nil, err
	}
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
	if err := ctx.Err(); err != nil {
		cleanup()
		return nil, err
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
	if err := os.Chmod(dataPath, storageFileMode); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to harden target file permissions: %w", err)
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
	if err := os.Chmod(metaPath, storageFileMode); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to harden metadata permissions: %w", err)
	}

	f.usage += written + int64(len(metaBytes))
	return asset, nil
}

func (f *FileStorage) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	sanitizedID, err := f.sanitizeID(id)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	asset, err := f.Stat(ctx, sanitizedID)
	if err != nil {
		return nil, err
	}
	if err := rejectSymlink(asset.Path); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
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

func (f *FileStorage) Delete(ctx context.Context, id string) error {
	sanitizedID, err := f.sanitizeID(id)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	dir := f.assetDir(sanitizedID)
	if err := rejectSymlink(dir); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	size, err := storageSizeAt(dir)
	if err != nil {
		return fmt.Errorf("failed to calculate asset size before delete: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("failed to delete asset: %w", err)
	}
	f.usage -= size
	if f.usage < 0 {
		f.usage = 0
	}
	return nil
}

func (f *FileStorage) Stat(ctx context.Context, id string) (*Asset, error) {
	sanitizedID, err := f.sanitizeID(id)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f.mu.RLock()
	defer f.mu.RUnlock()
	dir := f.assetDir(sanitizedID)
	if err := rejectSymlink(dir); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	metaPath := filepath.Join(dir, "meta.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err == nil {
		var asset Asset
		if jsonErr := json.Unmarshal(metaBytes, &asset); jsonErr == nil {
			if pathErr := f.validateAssetPath(dir, asset.Path); pathErr == nil {
				if symlinkErr := rejectSymlink(asset.Path); symlinkErr == nil {
					if _, statErr := os.Stat(asset.Path); statErr == nil {
						return &asset, nil
					}
				}
			}
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to read asset directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "meta.json" || strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := rejectSymlink(path); err != nil {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil {
			continue
		}
		return &Asset{ID: sanitizedID, Name: entry.Name(), Size: info.Size(), Path: path, CreatedAt: info.ModTime().UTC()}, nil
	}
	return nil, ErrNotFound
}
