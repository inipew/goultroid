package app

import (
	"context"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/database"
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

func TestApp_NewRollsBackResourcesAfterFeatureMigrationFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "invalid-feature-migration.db")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
		CREATE TABLE IF NOT EXISTS feature_schema_migrations (
			id TEXT PRIMARY KEY,
			description TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at DATETIME NOT NULL
		)`); err != nil {
		t.Fatalf("create feature migration table: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO feature_schema_migrations (id, description, checksum, applied_at)
		VALUES ('afk.001', 'invalid test migration', 'wrong-checksum', ?)`, time.Now().UTC()); err != nil {
		t.Fatalf("seed invalid feature migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}

	cfg := &config.Config{
		OwnerID:      123456,
		AppID:        123456,
		AppHash:      "hash123",
		Phone:        "+628123456789",
		SessionFile:  filepath.Join(filepath.Dir(dbPath), "session.json"),
		DatabasePath: dbPath,
		Prefix:       ".",
		LogLevel:     "error",
	}
	baseline := goruntime.NumGoroutine()
	const attempts = 5
	for range attempts {
		app, err := New(cfg)
		if err == nil || !strings.Contains(err.Error(), "feature migration checksum mismatch") {
			t.Fatalf("New() = (%v, %v), want feature migration checksum failure", app, err)
		}
	}

	deadline := time.Now().Add(time.Second)
	for goruntime.NumGoroutine() > baseline+2 && time.Now().Before(deadline) {
		goruntime.Gosched()
		time.Sleep(time.Millisecond)
	}
	if got := goruntime.NumGoroutine(); got > baseline+2 {
		t.Fatalf("New() failure leaked construction goroutines: baseline=%d after=%d", baseline, got)
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
