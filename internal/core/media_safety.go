package core

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
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
func CheckDiskSpace(path string, requiredBytes int64) error {
	if requiredBytes <= 0 {
		return nil
	}

	cleanPath := filepath.Clean(path)
	var stat syscall.Statfs_t
	if err := syscall.Statfs(cleanPath, &stat); err != nil {
		// If path doesn't exist yet, try parent directory
		parent := filepath.Dir(cleanPath)
		if err := syscall.Statfs(parent, &stat); err != nil {
			// If filesystem stats cannot be retrieved, do not block execution
			return nil
		}
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
