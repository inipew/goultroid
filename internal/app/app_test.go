package app

import (
	"context"
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
		OwnerID:      123456,
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
	t.Logf("registered plugins: %d", len(plugins))
	if len(plugins) < 30 {
		t.Errorf("expected at least 30 plugins registered, got %d", len(plugins))
	}

	// Test Shutdown with global budget
	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("unexpected error during shutdown: %v", err)
	}
}

func TestApp_UnifiedDAGComponents(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		OwnerID:      123456,
		AppID:        123456,
		AppHash:      "hash123",
		BotToken:     "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11",
		Phone:        "+628123456789",
		SessionFile:  filepath.Join(tmpDir, "session.json"),
		DatabasePath: filepath.Join(tmpDir, "test.db"),
		Prefix:       ".",
		LogLevel:     "error",
	}

	app, err := New(cfg)
	if err != nil {
		t.Fatalf("unexpected error creating app: %v", err)
	}

	expectedComponents := []string{
		"eventbus",
		"workers",
		"jobs",
		"scheduler",
		"callback_store",
		"inline_cache",
		"settings",
		"dispatcher",
		"assistant",
		"plugins",
	}

	for _, name := range expectedComponents {
		comp := app.runtime.Component(name)
		if comp == nil {
			t.Errorf("expected component %q to be registered in runtime DAG", name)
		}
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("unexpected error during shutdown: %v", err)
	}
}
