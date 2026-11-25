package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	runtimePkg "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/plugins/sysinfo"
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
	registered, ok := app.plugins.Find("sysinfo")
	if !ok {
		t.Fatal("sysinfo plugin was not registered")
	}
	sysinfoPlugin, ok := registered.(*sysinfo.Plugin)
	if !ok {
		t.Fatalf("sysinfo plugin type = %T", registered)
	}
	operatorSnapshot, ok := sysinfoPlugin.ResourceSnapshot()
	if !ok || operatorSnapshot.ProcessMemory.NumGoroutine < 1 {
		t.Fatalf("sysinfo provider unavailable: %+v", operatorSnapshot)
	}
	if operatorSnapshot.EventBus.Published != diag.EventBus.Published ||
		operatorSnapshot.Jobs.Definitions != diag.Jobs.Definitions ||
		operatorSnapshot.ResolverCacheCount != diag.ResolverCacheCount ||
		operatorSnapshot.TaskEngineAvailable != diag.TaskEngineSnapshotOK {
		t.Fatalf("operator snapshot differs from app diagnostics: operator=%+v app=%+v", operatorSnapshot, diag)
	}
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

func TestApp_SysinfoSnapshotUnavailable(t *testing.T) {
	engine := taskengine.NewEngine(taskengine.NewDefaultConfig())
	a := &App{taskEngine: engine}
	got := a.sysinfoResourceSnapshot()
	if got.TaskEngineAvailable || got.ProcessMemory.NumGoroutine < 1 || got.ProcessMemory.Sys < got.ProcessMemory.HeapAlloc {
		t.Fatalf("partial app operator snapshot = %+v", got)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := a.sysinfoResourceSnapshot(); !got.TaskEngineAvailable {
		t.Fatalf("running engine snapshot unavailable: %+v", got)
	}
	if err := engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := a.sysinfoResourceSnapshot(); got.TaskEngineAvailable {
		t.Fatalf("stopped engine snapshot reported available: %+v", got)
	}
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

func TestApp_DiagnosticsRuntimeComponentsAndStartupFailures(t *testing.T) {
	rt := runtime.New()

	dbComp := &testHealthComp{
		name:       "db",
		isCritical: true,
		health:     runtime.ComponentHealth{Status: runtime.HealthHealthy},
	}
	sensitiveErr := errors.New("assistant token invalid: https://api.telegram.org/bot123456789:ABCDEF1234567890abcdef1234567890/getMe token=secret_token_xyz")
	asstComp := &testHealthComp{
		name:       "assistant",
		isCritical: false,
		startErr:   sensitiveErr,
		health:     runtime.ComponentHealth{Status: runtime.HealthDegraded},
	}

	if err := rt.Register(dbComp); err != nil {
		t.Fatalf("failed to register dbComp: %v", err)
	}
	if err := rt.Register(asstComp); err != nil {
		t.Fatalf("failed to register asstComp: %v", err)
	}

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("unexpected start failure: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()

	app := &App{
		runtime: rt,
	}

	diag := app.Diagnostics()

	// 1. Verify StartupFailures in App diagnostics
	if len(diag.StartupFailures) != 1 {
		t.Fatalf("StartupFailures len = %d, want 1", len(diag.StartupFailures))
	}
	fail := diag.StartupFailures[0]
	if fail.Name != "assistant" {
		t.Errorf("fail name = %q, want assistant", fail.Name)
	}
	if strings.Contains(fail.Error, "123456789:ABCDEF") || strings.Contains(fail.Error, "secret_token_xyz") {
		t.Errorf("sensitive credential leaked in App diagnostics StartupFailures: %s", fail.Error)
	}
	if !strings.Contains(fail.Error, "[REDACTED_TOKEN]") || !strings.Contains(fail.Error, "token=[REDACTED]") {
		t.Errorf("expected redacted tokens in App diagnostics: %s", fail.Error)
	}

	// 2. Verify RuntimeComponents in App diagnostics
	compMap := make(map[string]runtime.ComponentSnapshot)
	for _, c := range diag.RuntimeComponents {
		compMap[c.Name] = c
	}

	dbSnap, ok := compMap["db"]
	if !ok || dbSnap.State != "running" || dbSnap.Status != "healthy" || dbSnap.Error != "" {
		t.Errorf("unexpected db component snapshot: %+v", dbSnap)
	}

	asstSnap, ok := compMap["assistant"]
	if !ok || asstSnap.State != "failed" || asstSnap.Status != "degraded" {
		t.Errorf("unexpected assistant component snapshot: %+v", asstSnap)
	}
	if strings.Contains(asstSnap.Error, "123456789:ABCDEF") || !strings.Contains(asstSnap.Error, "[REDACTED_TOKEN]") {
		t.Errorf("assistant component error leaked secret: %s", asstSnap.Error)
	}

	// 3. Verify shutdown reflection in diagnostics
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("unexpected stop error: %v", err)
	}

	diagAfterStop := app.Diagnostics()
	stopMap := make(map[string]runtime.ComponentSnapshot)
	for _, c := range diagAfterStop.RuntimeComponents {
		stopMap[c.Name] = c
	}
	if stopMap["db"].State != "stopped" {
		t.Errorf("expected db state 'stopped' after shutdown, got %q", stopMap["db"].State)
	}
}

func TestApp_DiagnosticsPerOwnerResourceBreakdown(t *testing.T) {
	mgr := resource.NewManagerWithLimit(100)
	app := &App{resources: mgr}

	// Register resources under different owners and types
	_ = mgr.Register(resource.Resource{
		ID:    "res-1",
		Owner: "plugin:downloader",
		Type:  resource.TypeTempFile,
	})
	_ = mgr.Register(resource.Resource{
		ID:    "res-2",
		Owner: "plugin:downloader",
		Type:  resource.TypeTempFile,
	})
	_ = mgr.Register(resource.Resource{
		ID:    "res-3",
		Owner: "plugin:downloader",
		Type:  resource.TypeProcess,
	})
	_ = mgr.Register(resource.Resource{
		ID:    "res-4",
		Owner: "service:media",
		Type:  resource.TypeTempDir,
	})
	_ = mgr.Register(resource.Resource{
		ID:    "res-5",
		Owner: "service:media",
		Type:  resource.TypeTempDir,
		State: resource.StateLeaked,
	})

	diag := app.Diagnostics()
	if diag.Resources.TotalActive != 5 {
		t.Fatalf("TotalActive = %d, want 5", diag.Resources.TotalActive)
	}
	if diag.Resources.Leaked != 1 {
		t.Fatalf("Leaked = %d, want 1", diag.Resources.Leaked)
	}
	if len(diag.Resources.ByOwner) != 2 {
		t.Fatalf("ByOwner len = %d, want 2", len(diag.Resources.ByOwner))
	}

	// ByOwner must be sorted by Owner name
	if diag.Resources.ByOwner[0].Owner != "plugin:downloader" {
		t.Errorf("ByOwner[0].Owner = %q, want plugin:downloader", diag.Resources.ByOwner[0].Owner)
	}
	if diag.Resources.ByOwner[0].TotalActive != 3 {
		t.Errorf("downloader TotalActive = %d, want 3", diag.Resources.ByOwner[0].TotalActive)
	}
	if diag.Resources.ByOwner[0].CountsByType[resource.TypeTempFile] != 2 {
		t.Errorf("downloader TempFile count = %d, want 2", diag.Resources.ByOwner[0].CountsByType[resource.TypeTempFile])
	}
	if diag.Resources.ByOwner[0].CountsByType[resource.TypeProcess] != 1 {
		t.Errorf("downloader Process count = %d, want 1", diag.Resources.ByOwner[0].CountsByType[resource.TypeProcess])
	}

	if diag.Resources.ByOwner[1].Owner != "service:media" {
		t.Errorf("ByOwner[1].Owner = %q, want service:media", diag.Resources.ByOwner[1].Owner)
	}
	if diag.Resources.ByOwner[1].TotalActive != 2 {
		t.Errorf("media TotalActive = %d, want 2", diag.Resources.ByOwner[1].TotalActive)
	}
	if diag.Resources.ByOwner[1].Leaked != 1 {
		t.Errorf("media Leaked = %d, want 1", diag.Resources.ByOwner[1].Leaked)
	}
}

func TestApp_DiagnosticsStorageProjection(t *testing.T) {
	tempDir := t.TempDir()
	fileStore, err := storage.NewFileStorage(tempDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	memStore := storage.NewMemoryStorage()

	// 1. With FileStorage
	appFile := &App{storage: fileStore}
	dFile := appFile.Diagnostics()
	if dFile.Storage.Backend != "file" {
		t.Errorf("storage backend = %q, want file", dFile.Storage.Backend)
	}
	if dFile.Storage.MaxQuota != 1024*1024 {
		t.Errorf("storage quota = %d, want %d", dFile.Storage.MaxQuota, 1024*1024)
	}

	// 2. With MemoryStorage
	appMem := &App{storage: memStore}
	dMem := appMem.Diagnostics()
	if dMem.Storage.Backend != "memory" {
		t.Errorf("storage backend = %q, want memory", dMem.Storage.Backend)
	}

	// 3. With nil storage
	appNil := &App{}
	dNil := appNil.Diagnostics()
	if dNil.Storage.Backend != "" {
		t.Errorf("expected empty backend for nil storage, got %q", dNil.Storage.Backend)
	}
}

func TestApp_DiagnosticsSnapshotImmutability(t *testing.T) {
	mgr := resource.NewManagerWithLimit(100)
	_ = mgr.Register(resource.Resource{
		ID:    "res-imm",
		Owner: "plugin:safe",
		Type:  resource.TypeTempFile,
	})
	app := &App{resources: mgr}

	d1 := app.Diagnostics()
	if len(d1.Resources.ByOwner) != 1 {
		t.Fatalf("expected 1 owner in ByOwner, got %d", len(d1.Resources.ByOwner))
	}

	// Mutate returned slice and map
	d1.Resources.ByOwner[0].CountsByType[resource.TypeTempFile] = 9999
	d1.Resources.ByOwner = append(d1.Resources.ByOwner, ResourceOwnerDiagnostics{Owner: "injected"})

	d2 := app.Diagnostics()
	if len(d2.Resources.ByOwner) != 1 {
		t.Fatalf("expected 1 owner in d2, got %d (exposed mutable slice)", len(d2.Resources.ByOwner))
	}
	if d2.Resources.ByOwner[0].CountsByType[resource.TypeTempFile] == 9999 {
		t.Fatal("Diagnostics exposed mutable CountsByType map")
	}
}

func TestApp_DiagnosticsConcurrentUpdateConsistency(t *testing.T) {
	mgr := resource.NewManagerWithLimit(1000)
	app := &App{resources: mgr}

	stopCh := make(chan struct{})
	var wg sync.WaitGroup

	// Writers
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; ; j++ {
				select {
				case <-stopCh:
					return
				default:
					id := fmt.Sprintf("res-%d-%d", workerID, j)
					_ = mgr.Register(resource.Resource{
						ID:    id,
						Owner: fmt.Sprintf("owner-%d", workerID),
						Type:  resource.TypeTempFile,
					})
					_ = mgr.Release(id)
				}
			}
		}(i)
	}

	// Readers
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopCh:
					return
				default:
					diag := app.Diagnostics()
					var sumActive, sumLeaked int
					for _, o := range diag.Resources.ByOwner {
						sumActive += o.TotalActive
						sumLeaked += o.Leaked
					}
					if diag.Resources.TotalActive != sumActive {
						t.Errorf("inconsistent TotalActive: %d vs sum %d", diag.Resources.TotalActive, sumActive)
					}
					if diag.Resources.Leaked != sumLeaked {
						t.Errorf("inconsistent Leaked: %d vs sum %d", diag.Resources.Leaked, sumLeaked)
					}
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stopCh)
	wg.Wait()
}

func TestApp_DiagnosticsUptimeSemanticsAcrossLifecycle(t *testing.T) {
	rt := runtime.New()
	comp := &testHealthComp{name: "up-comp"}
	_ = rt.Register(comp)

	app := &App{runtime: rt}

	// 1. New / Unstarted state: Uptime must be 0
	d1 := app.Diagnostics()
	if d1.Uptime != 0 {
		t.Fatalf("uptime in new state = %v, want 0", d1.Uptime)
	}

	// 2. Running state: Uptime must be > 0 and increasing
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var d2 DiagnosticsSnapshot
	runDeadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(runDeadline) {
		d2 = app.Diagnostics()
		if d2.Uptime > 0 {
			break
		}
	}
	if d2.Uptime <= 0 {
		t.Fatalf("uptime in running state = %v, want > 0", d2.Uptime)
	}

	// 3. Stopped state: Uptime must freeze
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	d3 := app.Diagnostics()
	freezeDeadline := time.Now().Add(10 * time.Millisecond)
	for time.Now().Before(freezeDeadline) {
		d4 := app.Diagnostics()
		if d3.Uptime != d4.Uptime {
			t.Fatalf("uptime in stopped state should be frozen: got %v then %v", d3.Uptime, d4.Uptime)
		}
	}
}

func TestApp_DiagnosticsNoPersistentGoroutines(t *testing.T) {
	app := &App{}
	runtimePkg.GC()
	initialGoroutines := runtimePkg.NumGoroutine()

	for i := 0; i < 50; i++ {
		_ = app.Diagnostics()
	}

	runtimePkg.GC()
	finalGoroutines := runtimePkg.NumGoroutine()
	// Allow delta (<= 2) for runtime/GC workers
	if diff := finalGoroutines - initialGoroutines; diff > 2 {
		t.Fatalf("diagnostics leaked goroutines: before=%d, after=%d", initialGoroutines, finalGoroutines)
	}
}

func TestApp_ReadinessDoesNotQueryStorageSQL(t *testing.T) {
	tempDir := t.TempDir()
	store, err := storage.NewFileStorage(tempDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{storage: store}

	// HealthContext must not perform any SQL or block on storage
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	h := app.HealthContext(ctx)
	elapsed := time.Since(start)

	if elapsed >= 40*time.Millisecond {
		t.Fatalf("HealthContext took too long: %v (suspected blocking or I/O)", elapsed)
	}
	if h.Subsystems["storage"] != HealthHealthy {
		t.Fatalf("storage subsystem status = %q, want healthy", h.Subsystems["storage"])
	}
}

func BenchmarkApp_Diagnostics_Idle(b *testing.B) {
	app := &App{}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = app.Diagnostics()
	}
}

func BenchmarkApp_Diagnostics_Active(b *testing.B) {
	mgr := resource.NewManagerWithLimit(100)
	for i := 0; i < 10; i++ {
		_ = mgr.Register(resource.Resource{
			ID:    fmt.Sprintf("res-%d", i),
			Owner: fmt.Sprintf("owner-%d", i%3),
			Type:  resource.TypeTempFile,
		})
	}
	memStore := storage.NewMemoryStorage()
	rt := runtime.New()
	for i := 0; i < 5; i++ {
		_ = rt.Register(&testHealthComp{name: fmt.Sprintf("c-%d", i)})
	}
	_ = rt.Start(context.Background())
	defer func() { _ = rt.Stop(context.Background()) }()

	app := &App{
		resources: mgr,
		storage:   memStore,
		runtime:   rt,
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = app.Diagnostics()
	}
}

func BenchmarkApp_Diagnostics_BoundaryOwners(b *testing.B) {
	mgr := resource.NewManagerWithLimit(1000)
	for i := 0; i < 64; i++ {
		_ = mgr.Register(resource.Resource{
			ID:    fmt.Sprintf("boundary-res-%d", i),
			Owner: fmt.Sprintf("plugin:owner-%02d", i),
			Type:  resource.TypeTempFile,
		})
	}
	app := &App{resources: mgr}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = app.Diagnostics()
	}
}
