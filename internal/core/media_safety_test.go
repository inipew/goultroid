package core

import (
	"errors"
	"os"
	"strings"
	"testing"
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
