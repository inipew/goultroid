package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateMediaSize(t *testing.T) {
	// Normal size within limit
	err := ValidateMediaSize(10*1024*1024, 150*1024*1024)
	if err != nil {
		t.Errorf("expected nil error for valid size, got %v", err)
	}

	// Size exceeding limit
	err = ValidateMediaSize(200*1024*1024, 150*1024*1024)
	if !errors.Is(err, ErrMediaTooLarge) {
		t.Errorf("expected ErrMediaTooLarge, got %v", err)
	}

	// Default fallback when maxSize <= 0
	err = ValidateMediaSize(600*1024*1024, 0)
	if !errors.Is(err, ErrMediaTooLarge) {
		t.Errorf("expected ErrMediaTooLarge with default limit, got %v", err)
	}
}

func TestCheckDiskSpace(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "goultroid-disk-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Asking for 1 byte should succeed on any working machine
	err = CheckDiskSpace(tmpDir, 1)
	if err != nil {
		t.Errorf("expected nil for 1 byte check, got %v", err)
	}

	// Asking for 0 or negative bytes should succeed immediately
	err = CheckDiskSpace(tmpDir, 0)
	if err != nil {
		t.Errorf("expected nil for 0 bytes check, got %v", err)
	}

	// Asking for an absurd amount (e.g. 100 Petabytes = 100 * 1024^5) should fail
	const absurdBytes = int64(100) * 1024 * 1024 * 1024 * 1024 * 1024
	err = CheckDiskSpace(tmpDir, absurdBytes)
	if !errors.Is(err, ErrInsufficientDiskSpace) {
		t.Errorf("expected ErrInsufficientDiskSpace for 100PB, got %v", err)
	}
}

func TestSanitizeFileName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal.jpg", "normal.jpg"},
		{"../../../etc/passwd", "passwd"},
		{"..", "media"},
		{".", "media"},
		{"/root/secret.mp4", "secret.mp4"},
		{"evil\x00file.png", "evilfile.png"},
		{"", "media"},
		{"   ", "media"},
	}

	for _, tc := range tests {
		result := SanitizeFileName(tc.input)
		if result != tc.expected {
			t.Errorf("SanitizeFileName(%q) = %q, expected %q", tc.input, result, tc.expected)
		}
	}

	// Test long filename truncation
	longName := strings.Repeat("a", 150) + ".mp3"
	truncated := SanitizeFileName(longName)
	if len(truncated) > 120 {
		t.Errorf("expected length <= 120, got %d", len(truncated))
	}
	if !strings.HasSuffix(truncated, ".mp3") {
		t.Errorf("expected .mp3 suffix to be preserved, got %s", truncated)
	}
}

func TestValidateUploadSize(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "sample.bin")
	if err := os.WriteFile(testFile, make([]byte, 1024), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Size within limit
	if err := ValidateUploadSize(testFile, 2048); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Size exceeds limit
	if err := ValidateUploadSize(testFile, 512); !errors.Is(err, ErrMediaTooLarge) {
		t.Errorf("expected ErrMediaTooLarge, got %v", err)
	}

	// Non-existent file should return nil (non-blocking for mock tests)
	if err := ValidateUploadSize(filepath.Join(tmpDir, "missing.bin"), 1024); err != nil {
		t.Errorf("expected nil error for missing file in ValidateUploadSize, got: %v", err)
	}
}

func TestEnforceDirectoryQuota(t *testing.T) {
	tmpDir := t.TempDir()

	// Create 3 files with artificial timestamps
	f1 := filepath.Join(tmpDir, "file1.bin")
	f2 := filepath.Join(tmpDir, "file2.bin")
	f3 := filepath.Join(tmpDir, "file3.bin")

	_ = os.WriteFile(f1, make([]byte, 100), 0644)
	_ = os.WriteFile(f2, make([]byte, 100), 0644)
	_ = os.WriteFile(f3, make([]byte, 100), 0644)

	// Set distinct modification times: f1 oldest, then f2, f3 newest
	now := time.Now()
	_ = os.Chtimes(f1, now.Add(-3*time.Hour), now.Add(-3*time.Hour))
	_ = os.Chtimes(f2, now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	_ = os.Chtimes(f3, now.Add(-1*time.Hour), now.Add(-1*time.Hour))

	// Max total quota 150 bytes: should evict f1 first so total becomes <= 150 bytes
	if err := EnforceDirectoryQuota(tmpDir, 150, 0); err != nil {
		t.Fatalf("EnforceDirectoryQuota error: %v", err)
	}

	if _, err := os.Stat(f1); !os.IsNotExist(err) {
		t.Errorf("expected f1 to be evicted")
	}
	if _, err := os.Stat(f3); err != nil {
		t.Errorf("expected f3 to still exist: %v", err)
	}

	// Test maxAge eviction: files older than 90m should be removed (f2 should be removed)
	if err := EnforceDirectoryQuota(tmpDir, 1000, 90*time.Minute); err != nil {
		t.Fatalf("EnforceDirectoryQuota error: %v", err)
	}
	if _, err := os.Stat(f2); !os.IsNotExist(err) {
		t.Errorf("expected f2 to be evicted due to age")
	}
	if _, err := os.Stat(f3); err != nil {
		t.Errorf("expected f3 to still exist: %v", err)
	}
}
