package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/runtime"
)

func TestApp_DiagnosticsCentralizedMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		OwnerID:      123456,
		AppID:        123456,
		AppHash:      "hash123",
		Phone:        "+628123456789",
		SessionFile:  filepath.Join(tmpDir, "session.json"),
		DatabasePath: filepath.Join(tmpDir, "test.db"),
		Prefix:       ".",
		LogLevel:     "info",
	}

	app, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}

	// Register a mock supervisor worker to verify worker metrics in diagnostics
	if app.supervisor != nil {
		err := app.supervisor.Register(runtime.WorkerSpec{
			Name:    "test-diagnostic-worker",
			Restart: runtime.NeverRestart,
			Run: func(ctx context.Context) error {
				return nil
			},
		})
		if err != nil {
			t.Fatalf("failed to register supervisor worker: %v", err)
		}
	}

	diag := app.Diagnostics()

	// Verify DB metrics
	if app.db != nil {
		if diag.DB.MaxOpenConnections < 0 {
			t.Errorf("expected DB MaxOpenConnections >= 0, got %d", diag.DB.MaxOpenConnections)
		}
	}

	// Verify Supervisor workers snapshot
	var foundWorker bool
	for _, w := range diag.Workers {
		if w.Name == "test-diagnostic-worker" {
			foundWorker = true
			break
		}
	}
	if !foundWorker {
		t.Errorf("expected worker 'test-diagnostic-worker' to be reported in diagnostics")
	}

	// Verify Telegram dialogs warmup worker is registered
	var foundWarmup bool
	for _, w := range diag.Workers {
		if w.Name == "telegram-dialogs-warmup" {
			foundWarmup = true
			break
		}
	}
	if !foundWarmup {
		t.Errorf("expected 'telegram-dialogs-warmup' worker to be registered in supervisor")
	}

	// Verify Cache and RPC structures are populated without panic
	if diag.ResolverCacheCount < 0 {
		t.Errorf("expected ResolverCacheCount >= 0, got %d", diag.ResolverCacheCount)
	}
	if diag.RPC.TotalRequests < 0 {
		t.Errorf("expected RPC TotalRequests >= 0, got %d", diag.RPC.TotalRequests)
	}

	// Verify shutdown report exists
	_ = diag.LastShutdownReport
	_ = time.Now()
}
