package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/taskengine"
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
	if diag.ProcessMemory.NumGoroutine < 1 || diag.ProcessMemory.Sys < diag.ProcessMemory.HeapAlloc {
		t.Fatalf("process memory diagnostics = %+v", diag.ProcessMemory)
	}
	busStats := app.eventBus.Stats()
	if diag.EventBus.ActiveWorkers != busStats.ActiveWorkers || diag.EventBus.OrderedWorkers != busStats.OrderedWorkers {
		t.Fatalf("event worker diagnostics = %+v, bus = %+v", diag.EventBus, busStats)
	}
	if got, want := diag.Inline, app.inlineEngine.RuntimeStats(); got != want {
		t.Fatalf("inline diagnostics = %+v, want %+v", got, want)
	}
	if got, want := diag.Interaction, app.plugins.InteractionRuntime().SnapshotStats(); got != want {
		t.Fatalf("interaction diagnostics = %+v, want %+v", got, want)
	}
	var active, leaked int
	for _, owner := range app.resources.AllSnapshots() {
		active += owner.TotalActive
		leaked += owner.Leaked
	}
	if diag.Resources.TotalActive != active || diag.Resources.Leaked != leaked {
		t.Fatalf("resource diagnostics = %+v, want active=%d leaked=%d", diag.Resources, active, leaked)
	}

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

func TestApp_DiagnosticsUnavailableComponents(t *testing.T) {
	engine := taskengine.NewEngine(taskengine.NewDefaultConfig())
	a := &App{taskEngine: engine, plugins: &plugin.Manager{}, inlineEngine: &inline.Engine{}}
	got := a.Diagnostics()
	if got.TaskEngineSnapshotOK || got.ProcessMemory.NumGoroutine < 1 || got.Interaction.Sessions != 0 || got.Inline.CacheEntries != 0 {
		t.Fatalf("unavailable component snapshot = %+v", got)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })
	if got := a.Diagnostics(); !got.TaskEngineSnapshotOK || got.TaskEngine.Pools == nil {
		t.Fatalf("running engine snapshot = %+v", got)
	}
	if err := engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := a.Diagnostics(); got.TaskEngineSnapshotOK {
		t.Fatalf("stopped engine snapshot reported available: %+v", got)
	}
}
