package app

import (
	"path/filepath"
	"testing"

	"github.com/inipew/goultroid/internal/config"
)

func TestApp_New(t *testing.T) {
	// Nil config
	if _, err := New(nil); err == nil {
		t.Errorf("expected error when config is nil")
	}

	// Valid config
	tmpDir := t.TempDir()
	cfg := &config.Config{
		AppID:        123456,
		AppHash:      "hash123",
		Phone:        "+628123456789",
		SessionFile:  filepath.Join(tmpDir, "session.json"),
		DatabasePath: filepath.Join(tmpDir, "test.db"),
		Prefix:       ".",
		LogLevel:     "debug",
	}

	app, err := New(cfg)
	if err != nil {
		t.Fatalf("unexpected error creating app: %v", err)
	}

	if app == nil {
		t.Fatal("expected app instance, got nil")
	}

	// Verify plugins were registered
	plugins := app.plugins.Plugins()
	if len(plugins) < 19 {
		t.Errorf("expected at least 19 plugins registered, got %d", len(plugins))
	}

	// Test Shutdown
	if err := app.Shutdown(); err != nil {
		t.Errorf("unexpected error during shutdown: %v", err)
	}
}
