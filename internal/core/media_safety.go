package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

var (
	// ErrMediaTooLarge indicates that a media file exceeds the allowed processing threshold.
	ErrMediaTooLarge = errors.New("media file size exceeds maximum allowed limit")
	// ErrInsufficientDiskSpace indicates that available disk space is below required bytes.
	ErrInsufficientDiskSpace = errors.New("insufficient disk space for media operation")
)

const (
	// DefaultMaxExtractAudioSize is the maximum file size allowed for audio extraction (150 MB).
	DefaultMaxExtractAudioSize = 150 * 1024 * 1024
	// DefaultMaxDownloadSize is the maximum file size allowed for generic downloads (500 MB).
	DefaultMaxDownloadSize = 500 * 1024 * 1024
	// DefaultMaxUploadSize is the maximum file size allowed for generic uploads (500 MB).
	DefaultMaxUploadSize = 500 * 1024 * 1024
	// DefaultDirectoryQuota is the maximum total disk quota for the downloads directory (2 GB).
	DefaultDirectoryQuota = 2 * 1024 * 1024 * 1024
	// DefaultMaxFileAge is the maximum age of downloaded files before eviction (24 hours).
	DefaultMaxFileAge = 24 * time.Hour
)

// ValidateMediaSize checks if the given file size is within the allowed maximum limit.
func ValidateMediaSize(size int64, maxSize int64) error {
	if maxSize <= 0 {
		maxSize = DefaultMaxDownloadSize
	}
	if size > maxSize {
		return fmt.Errorf("%w: %d bytes (limit: %d bytes)", ErrMediaTooLarge, size, maxSize)
	}
	return nil
}

// CheckDiskSpace verifies if the target directory's filesystem has at least requiredBytes available.
// If requiredBytes <= 0, a conservative safety threshold of 50 MB is enforced.
// It resolves the nearest existing ancestor path for Statfs and fails closed on error.
func CheckDiskSpace(path string, requiredBytes int64) error {
	if requiredBytes <= 0 {
		requiredBytes = 50 * 1024 * 1024
	}

	cleanPath := filepath.Clean(path)
	var stat syscall.Statfs_t
	current := cleanPath
	var statErr error
	found := false

	for {
		if err := syscall.Statfs(current, &stat); err == nil {
			found = true
			break
		} else {
			statErr = err
		}
		parent := filepath.Dir(current)
		if parent == current || parent == "." {
			if err := syscall.Statfs(".", &stat); err == nil {
				found = true
			}
			break
		}
		current = parent
	}

	if !found {
		return fmt.Errorf("%w: failed to inspect filesystem for %q: %v", ErrInsufficientDiskSpace, path, statErr)
	}

	// Available bytes to non-root users = Bavail * Bsize
	availableBytes := int64(stat.Bavail) * int64(stat.Bsize)
	if availableBytes < requiredBytes {
		return fmt.Errorf("%w: available %d bytes, required %d bytes", ErrInsufficientDiskSpace, availableBytes, requiredBytes)
	}

	return nil
}

// SanitizeFileName cleans and strips dangerous characters or path separators from a file name.
func SanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	name = filepath.Base(filepath.Clean(name))

	// Remove common path traversal artifacts
	for name == "." || name == ".." || name == "/" || name == "\\" {
		name = "media"
	}

	// Filter out invalid characters
	invalidChars := []string{"\x00", "\n", "\r", "\t", ".."}
	for _, char := range invalidChars {
		name = strings.ReplaceAll(name, char, "")
	}

	if name == "" {
		name = "media"
	}

	// Restrict length to prevent filesystem limit errors
	if len(name) > 120 {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if len(base) > 100 {
			base = base[:100]
		}
		name = base + ext
	}

	return name
}

// ValidateUploadSize checks if the given local file size is within the allowed upload limit.
// If the file exists, its size must not exceed maxUploadSize.
func ValidateUploadSize(filePath string, maxUploadSize int64) error {
	if maxUploadSize <= 0 {
		maxUploadSize = DefaultMaxUploadSize
	}
	stat, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to inspect file %q: %w", filePath, err)
	}
	if stat.Size() > maxUploadSize {
		return fmt.Errorf("%w: file %q (%d bytes) exceeds upload limit (%d bytes)", ErrMediaTooLarge, filePath, stat.Size(), maxUploadSize)
	}
	return nil
}

// EnforceDirectoryQuota cleans up files in dir if total size exceeds maxTotalBytes or files exceed maxAge.
// Uses FIFO eviction (oldest modified files deleted first).
func EnforceDirectoryQuota(dir string, maxTotalBytes int64, maxAge time.Duration) error {
	if maxTotalBytes <= 0 {
		maxTotalBytes = DefaultDirectoryQuota
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	type fileItem struct {
		path    string
		size    int64
		modTime time.Time
	}

	var files []fileItem
	now := time.Now()
	var totalSize int64

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(dir, entry.Name())
		// Evict expired files first
		if maxAge > 0 && now.Sub(info.ModTime()) > maxAge {
			_ = os.Remove(p)
			continue
		}
		files = append(files, fileItem{
			path:    p,
			size:    info.Size(),
			modTime: info.ModTime(),
		})
		totalSize += info.Size()
	}

	if totalSize <= maxTotalBytes {
		return nil
	}

	// Sort oldest first
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	for _, f := range files {
		if totalSize <= maxTotalBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			totalSize -= f.size
		}
	}

	return nil
}

