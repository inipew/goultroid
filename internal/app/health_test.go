package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/runtime"
	mediaSvc "github.com/inipew/goultroid/internal/services/media"
	"github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/telegram"
)

func TestHealth_NotRunningIsNotReady(t *testing.T) {
	a := &App{}
	health := a.Health()
	if health.Ready {
		t.Fatal("new application must not be ready")
	}
	if health.Status != HealthDegraded {
		t.Fatalf("new application status = %q, want degraded", health.Status)
	}
	if !health.HasReason("runtime:not-running:") {
		t.Fatalf("missing lifecycle reason: %v", health.Reasons)
	}
}

func TestHealth_RunningIsHealthy(t *testing.T) {
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	a := &App{eventBus: bus, startTime: time.Now()}
	a.lifecycleState.Store(uint32(lifecycleRunning))
	health := a.Health()
	if !health.Ready || !health.Healthy() {
		t.Fatalf("running app health = %+v", health)
	}
}

func TestHealth_StoppedWithPluginResourcesIsUnhealthy(t *testing.T) {
	a := &App{}
	a.lifecycleState.Store(uint32(lifecycleStopped))
	health := a.Health()
	if health.Ready || health.Status != HealthUnhealthy {
		t.Fatalf("stopped app health = %+v", health)
	}
}

func TestHealthContext_ReportsDatabase(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &App{db: db}
	a.lifecycleState.Store(uint32(lifecycleRunning))
	health := a.HealthContext(context.Background())
	if got := health.Subsystems["database"]; got != HealthHealthy {
		t.Fatalf("database health = %q, want %q", got, HealthHealthy)
	}
}

func TestHealthContext_DatabaseFailureMakesAppUnreadyAndUnhealthy(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// Close DB so PingContext fails
	_ = db.Close()

	a := &App{db: db}
	a.lifecycleState.Store(uint32(lifecycleRunning))
	health := a.HealthContext(context.Background())

	if got := health.Subsystems["database"]; got != HealthUnhealthy {
		t.Fatalf("database subsystem health = %q, want %q", got, HealthUnhealthy)
	}
	if health.Ready {
		t.Fatal("app must not be ready when database ping fails")
	}
	if health.Status != HealthUnhealthy {
		t.Fatalf("app health status = %q, want %q", health.Status, HealthUnhealthy)
	}
	if !health.HasReason("database:ping-failed") {
		t.Fatalf("missing database:ping-failed reason, got: %v", health.Reasons)
	}
}

func TestHealthContext_DatabaseHealthNotOverwrittenByRuntime(t *testing.T) {
	rt := runtime.New()
	comp := resourceComponent{
		name: "database",
		health: func(context.Context) runtime.ComponentHealth {
			return runtime.ComponentHealth{
				Status:  runtime.HealthUnhealthy,
				Details: "ping-failed",
				Error:   errors.New("db disconnected"),
			}
		},
	}
	_ = rt.Register(comp)
	_ = rt.Start(context.Background())
	defer rt.Stop(context.Background())

	a := &App{runtime: rt}
	a.lifecycleState.Store(uint32(lifecycleRunning))
	health := a.HealthContext(context.Background())

	if got := health.Subsystems["database"]; got != HealthUnhealthy {
		t.Fatalf("database subsystem health was overwritten: got %q, want %q", got, HealthUnhealthy)
	}
	if health.Ready {
		t.Fatal("app must not be ready when runtime database component is unhealthy")
	}
	if health.Status != HealthUnhealthy {
		t.Fatalf("app health status = %q, want %q", health.Status, HealthUnhealthy)
	}
}

type testHealthComp struct {
	name       string
	deps       []string
	isCritical bool
	health     runtime.ComponentHealth
}

func (c *testHealthComp) Name() string                                   { return c.name }
func (c *testHealthComp) Dependencies() []string                         { return c.deps }
func (c *testHealthComp) Start(context.Context) error                    { return nil }
func (c *testHealthComp) Stop(context.Context) error                     { return nil }
func (c *testHealthComp) Health(context.Context) runtime.ComponentHealth { return c.health }
func (c *testHealthComp) IsCritical() bool                               { return c.isCritical }

func TestHealthContext_RuntimeOptionalFailureDegradesWithoutMakingAppNotReady(t *testing.T) {
	rt := runtime.New()
	crit := &testHealthComp{
		name:       "db",
		isCritical: true,
		health:     runtime.ComponentHealth{Status: runtime.HealthHealthy},
	}
	opt := &testHealthComp{
		name:       "assistant",
		isCritical: false,
		health: runtime.ComponentHealth{
			Status:  runtime.HealthUnhealthy,
			Details: "token-invalid",
		},
	}
	_ = rt.Register(crit)
	_ = rt.Register(opt)
	_ = rt.Start(context.Background())
	defer rt.Stop(context.Background())

	a := &App{runtime: rt}
	a.lifecycleState.Store(uint32(lifecycleRunning))
	health := a.HealthContext(context.Background())

	if !health.Ready {
		t.Fatal("app must remain ready when only optional component is unhealthy")
	}
	if health.Status != HealthDegraded {
		t.Fatalf("app health status = %q, want %q", health.Status, HealthDegraded)
	}
	if health.Subsystems["assistant"] != HealthUnhealthy {
		t.Fatalf("assistant subsystem status = %q, want %q", health.Subsystems["assistant"], HealthUnhealthy)
	}
}

func TestHealthContext_ShutdownStateIsUnhealthyAndNotReady(t *testing.T) {
	a := &App{}
	a.lifecycleState.Store(uint32(lifecycleStopping))
	health := a.Health()
	if health.Ready {
		t.Fatal("stopping app must not be ready")
	}
	if health.Status != HealthDegraded {
		t.Fatalf("stopping app status = %q, want %q", health.Status, HealthDegraded)
	}
	if !health.HasReason("runtime:not-running:stopping") {
		t.Fatalf("missing stopping reason, got: %v", health.Reasons)
	}

	a.lifecycleState.Store(uint32(lifecycleStopped))
	health = a.Health()
	if health.Ready {
		t.Fatal("stopped app must not be ready")
	}
	if health.Status != HealthUnhealthy {
		t.Fatalf("stopped app status = %q, want %q", health.Status, HealthUnhealthy)
	}
	if !health.HasReason("runtime:not-running:stopped") {
		t.Fatalf("missing stopped reason, got: %v", health.Reasons)
	}
}

func TestProbeFilesystem_BoundedAndContextAware(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Valid directory with active context -> Healthy
	status, reason := probeFilesystem(context.Background(), tempDir)
	if status != HealthHealthy || reason != "" {
		t.Fatalf("expected healthy, got %s, reason: %s", status, reason)
	}

	// 2. Pre-cancelled context -> HealthUnknown, probe-timeout
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	status, reason = probeFilesystem(ctx, tempDir)
	if time.Since(start) > 50*time.Millisecond {
		t.Fatalf("probe took %v, want < 50ms", time.Since(start))
	}
	if status != HealthUnknown || reason != "filesystem:probe-timeout" {
		t.Fatalf("expected unknown probe-timeout, got status=%q, reason=%q", status, reason)
	}

	// 3. Nonexistent directory -> HealthUnhealthy, filesystem:unavailable
	nonexistent := filepath.Join(tempDir, "does-not-exist")
	status, reason = probeFilesystem(context.Background(), nonexistent)
	if status != HealthUnhealthy || reason != "filesystem:unavailable" {
		t.Fatalf("expected unhealthy filesystem:unavailable, got status=%q, reason=%q", status, reason)
	}
}

func TestProbeFilesystem_ResourceBoundedWhenStuck(t *testing.T) {
	hangCh := make(chan struct{})

	// Simulate hanging filesystem call using thread-safe setter
	restore := setFSProbeStatFn(func(name string) (os.FileInfo, error) {
		<-hangCh
		return nil, os.ErrNotExist
	})
	defer restore()
	defer func() {
		select {
		case <-hangCh:
		default:
			close(hangCh)
		}
		// Wait for hanging probe goroutine to release slot before completing test
		for i := 0; i < 50; i++ {
			if len(fsProbeSlot) == 0 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// First probe hangs and times out
	ctx1, cancel1 := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel1()

	st1, re1 := probeFilesystem(ctx1, "/any/path")
	if st1 != HealthUnknown || re1 != "filesystem:probe-timeout" {
		t.Fatalf("first hung probe status=%q, reason=%q, want unknown/probe-timeout", st1, re1)
	}

	// Record baseline goroutine count with one hanging probe in flight
	g0 := stdruntime.NumGoroutine()

	// Rapidly call probeFilesystem 20 times while the first probe is still hung.
	// All subsequent calls must return filesystem:probe-saturated immediately without spawning goroutines.
	for i := 0; i < 20; i++ {
		st, re := probeFilesystem(context.Background(), "/any/path")
		if st != HealthUnknown || re != "filesystem:probe-saturated" {
			t.Fatalf("call %d: status=%q, reason=%q, want unknown/probe-saturated", i, st, re)
		}
	}

	// Goroutine count must not have accumulated unbounded goroutines
	gCurrent := stdruntime.NumGoroutine()
	if gCurrent > g0+2 {
		t.Fatalf("goroutine leak: before=%d, current=%d (expected <= %d)", g0, gCurrent, g0+2)
	}
}

func TestSanitizeHealthIdentifier_UntrustedInputs(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "valid-name", want: "valid-name"},
		{input: "Task With Spaces (10/20)", want: "task-with-spaces"},
		{input: "https://api.telegram.org/bot1234:ABC?token=secret", want: "https-api-telegram-org-bot1234-abc"},
		{input: "../../etc/passwd", want: "etc-passwd"},
		{input: "owner_with_special$#@%*chars", want: "owner-with-special-chars"},
		{input: "a-very-long-task-name-that-exceeds-the-maximum-bounded-length-allowed-by-sanitizer", want: "a-very-long-task-name-that-exceeds-the-maximum-b"},
		{input: "   ", want: "unknown"},
		{input: "???", want: "unknown"},
	}

	for _, tc := range tests {
		got := sanitizeHealthIdentifier(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeHealthIdentifier(%q) = %q, want %q", tc.input, got, tc.want)
		}
		if len(got) > 48 {
			t.Errorf("sanitizeHealthIdentifier(%q) length %d > 48", tc.input, len(got))
		}
	}
}

func TestHealthContext_DependencyUnavailable(t *testing.T) {
	bus := core.NewEventBus()
	// EventBus created but not started -> "eventbus:not-started"
	a := &App{eventBus: bus}
	a.lifecycleState.Store(uint32(lifecycleRunning))

	h := a.HealthContext(context.Background())
	if h.Subsystems["eventbus"] != HealthDegraded {
		t.Fatalf("subsystem = %q, want degraded", h.Subsystems["eventbus"])
	}
	if !h.HasReason("eventbus:not-started") {
		t.Fatalf("missing eventbus:not-started reason: %v", h.Reasons)
	}

	// Close event bus -> "eventbus:closed", status Unhealthy
	_ = bus.Start(context.Background())
	bus.Close()

	h = a.HealthContext(context.Background())
	if h.Subsystems["eventbus"] != HealthUnhealthy {
		t.Fatalf("subsystem = %q, want unhealthy", h.Subsystems["eventbus"])
	}
	if !h.HasReason("eventbus:closed") {
		t.Fatalf("missing eventbus:closed reason: %v", h.Reasons)
	}
}

func TestHealthContext_ConcurrentStateTransitions(t *testing.T) {
	bus := core.NewEventBus()
	_ = bus.Start(context.Background())
	defer bus.Close()

	a := &App{eventBus: bus, startTime: time.Now()}
	a.lifecycleState.Store(uint32(lifecycleRunning))

	stopCh := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-stopCh:
					return
				default:
					if id%2 == 0 {
						a.lifecycleState.Store(uint32(lifecycleRunning))
					} else {
						a.lifecycleState.Store(uint32(lifecycleStopping))
					}
					time.Sleep(time.Millisecond)
				}
			}
		}(i)
	}

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				h := a.HealthContext(context.Background())
				if h.Status == "" {
					t.Errorf("empty health status returned")
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stopCh)
	wg.Wait()
}

func TestHealthContext_QueuePressure(t *testing.T) {
	// 1. Process concurrency saturation
	runner := process.NewOSRunner(1, 2*time.Second, 1024)
	appProcess := &App{processRunner: runner}
	appProcess.lifecycleState.Store(uint32(lifecycleRunning))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	procStarted := make(chan struct{})
	go func() {
		close(procStarted)
		_, _ = runner.Run(ctx, process.Request{Command: "sleep", Args: []string{"2"}})
	}()

	<-procStarted
	for i := 0; i < 50; i++ {
		if runner.Diagnostics().Active >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	hProc := appProcess.HealthContext(context.Background())
	if hProc.Subsystems["process"] != HealthDegraded {
		t.Fatalf("process subsystem = %q, want %q", hProc.Subsystems["process"], HealthDegraded)
	}
	if !hProc.HasReason("process:concurrency-saturated") {
		t.Fatalf("missing process:concurrency-saturated reason: %v", hProc.Reasons)
	}
	if hProc.Status != HealthDegraded {
		t.Fatalf("app status = %q, want degraded", hProc.Status)
	}
	if !hProc.Ready {
		t.Fatal("process concurrency saturation must degrade health, not make app unready")
	}
	cancel()

	// 2. Telegram client readiness (testing both unready and ready via immutable test client)
	dispatcher := &telegram.Dispatcher{}
	tgClientUnready := telegram.NewClientForTesting(dispatcher, false)

	appCmd := &App{client: tgClientUnready}
	appCmd.lifecycleState.Store(uint32(lifecycleRunning))

	// Initially client is not ready
	hCmd := appCmd.HealthContext(context.Background())
	if hCmd.Subsystems["telegram"] != HealthDegraded {
		t.Fatalf("telegram subsystem = %q, want degraded", hCmd.Subsystems["telegram"])
	}
	if !hCmd.HasReason("telegram:not-ready") {
		t.Fatalf("missing telegram:not-ready reason: %v", hCmd.Reasons)
	}
	if hCmd.Ready {
		t.Fatal("app must not be ready when telegram client is not ready")
	}

	// Ready client
	tgClientReady := telegram.NewClientForTesting(dispatcher, true)
	appCmd.client = tgClientReady
	hCmd = appCmd.HealthContext(context.Background())
	if hCmd.Subsystems["telegram"] != HealthHealthy {
		t.Fatalf("telegram subsystem = %q, want healthy", hCmd.Subsystems["telegram"])
	}
	if hCmd.HasReason("telegram:not-ready") {
		t.Fatalf("unexpected telegram:not-ready reason: %v", hCmd.Reasons)
	}
	if !hCmd.Ready {
		t.Fatal("app must be ready after telegram client is ready")
	}

	// 3. Command concurrency via TaskEngine admission owner & Dispatcher execution lifecycle
	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"interactive": {Concurrency: 1, BacklogLimit: 4},
		},
		ResultCapacity: 10,
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("failed to start task engine: %v", err)
	}
	defer engine.Stop(context.Background())

	dispatcher.SetTasks(engine)
	exec := core.NewCommandExecutor(nil, nil, 5*time.Second)
	dispatcher.SetExecutor(exec)
	appCmd.taskEngine = engine

	// Verify baseline: capacity is 1 (from TaskEngine interactive pool), active is 0
	hCmd = appCmd.HealthContext(context.Background())
	if hCmd.Subsystems["commands"] != HealthHealthy {
		t.Fatalf("commands subsystem = %q, want healthy", hCmd.Subsystems["commands"])
	}

	// Execute an interactive command that blocks while running
	cmdBlocker := make(chan struct{})
	cmdRunning := make(chan struct{})
	testCmd := core.Command{
		Name: "test-cmd",
		Handler: func(c *core.Context) error {
			close(cmdRunning)
			<-cmdBlocker
			return nil
		},
	}

	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	defer cmdCancel()
	coreCtx := &core.Context{Ctx: context.Background()}

	submitErr := dispatcher.SubmitInteractiveCommandForTesting(cmdCtx, cmdCancel, coreCtx, testCmd, "cmd-test-1", "user-1", "")
	if submitErr != nil {
		t.Fatalf("failed to submit interactive command: %v", submitErr)
	}

	// Wait until command is actively running inside worker
	<-cmdRunning

	// Now active is 1 and capacity is 1 -> concurrency saturated
	hCmd = appCmd.HealthContext(context.Background())
	if hCmd.Subsystems["commands"] != HealthDegraded {
		t.Fatalf("commands subsystem = %q, want degraded", hCmd.Subsystems["commands"])
	}
	if !hCmd.HasReason("commands:concurrency-saturated") {
		t.Fatalf("missing commands:concurrency-saturated reason: %v", hCmd.Reasons)
	}
	if hCmd.Status != HealthDegraded {
		t.Fatalf("app status = %q, want degraded", hCmd.Status)
	}
	if !hCmd.Ready {
		t.Fatal("command concurrency saturation must degrade health, not make app unready")
	}

	// Complete command
	close(cmdBlocker)
	for i := 0; i < 50; i++ {
		if dispatcher.RunningCommands() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if dispatcher.RunningCommands() != 0 {
		t.Fatalf("running commands = %d, want 0 after completion", dispatcher.RunningCommands())
	}

	hCmd = appCmd.HealthContext(context.Background())
	if hCmd.Subsystems["commands"] != HealthHealthy {
		t.Fatalf("commands subsystem = %q, want healthy after completion", hCmd.Subsystems["commands"])
	}
	if hCmd.HasReason("commands:concurrency-saturated") {
		t.Fatalf("unexpected commands:concurrency-saturated reason after completion: %v", hCmd.Reasons)
	}

	// Also verify active command count is released on panic
	panicCmd := core.Command{
		Name: "panic-cmd",
		Handler: func(c *core.Context) error {
			panic("simulated command panic")
		},
	}
	panicCtx, panicCancel := context.WithCancel(context.Background())
	defer panicCancel()
	_ = dispatcher.SubmitInteractiveCommandForTesting(panicCtx, panicCancel, coreCtx, panicCmd, "cmd-panic-1", "user-1", "")

	for i := 0; i < 50; i++ {
		if dispatcher.RunningCommands() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if dispatcher.RunningCommands() != 0 {
		t.Fatalf("running commands = %d, want 0 after panic", dispatcher.RunningCommands())
	}

	// 3. EventBus dropped events and handler panics
	bus := core.NewEventBus()
	appBus := &App{eventBus: bus}
	appBus.lifecycleState.Store(uint32(lifecycleRunning))

	blocker := make(chan struct{})
	bus.Subscribe(core.EventTypeMessageCreated, func(e core.Event) {
		<-blocker
	})
	_ = bus.Start(context.Background())

	// Occupy all general workers (eventWorkers = 8) and fill queue (1024)
	for i := 0; i < 1050; i++ {
		bus.Publish(&core.MessageCreatedEvent{})
	}

	close(blocker)

	hBus := appBus.HealthContext(context.Background())
	if !hBus.HasReason("eventbus:dropped-events") {
		t.Fatalf("missing eventbus:dropped-events reason: %v", hBus.Reasons)
	}
	if hBus.Status != HealthDegraded {
		t.Fatalf("status = %q, want degraded", hBus.Status)
	}
	_ = bus.Close()

	// Handler panics
	busPanic := core.NewEventBus()
	appPanic := &App{eventBus: busPanic}
	appPanic.lifecycleState.Store(uint32(lifecycleRunning))
	busPanic.Subscribe(core.EventTypeMessageCreated, func(e core.Event) {
		panic("test panic")
	})
	_ = busPanic.Start(context.Background())
	_ = busPanic.PublishDurable(context.Background(), &core.MessageCreatedEvent{})
	hPanic := appPanic.HealthContext(context.Background())
	if !hPanic.HasReason("eventbus:handler-panics") {
		t.Fatalf("missing eventbus:handler-panics reason: %v", hPanic.Reasons)
	}
	if hPanic.Status != HealthDegraded {
		t.Fatalf("status = %q, want degraded", hPanic.Status)
	}
	_ = busPanic.Close()
}

type testBlockingReader struct {
	data        []byte
	blockCh     chan struct{}
	readStarted chan struct{}
	readOnce    bool
}

func (b *testBlockingReader) Read(p []byte) (n int, err error) {
	if !b.readOnce {
		b.readOnce = true
		if b.readStarted != nil {
			close(b.readStarted)
		}
		<-b.blockCh
	}
	return copy(p, b.data), io.EOF
}

func TestHealthContext_DoesNotWaitOnStorageMutex(t *testing.T) {
	tempDir := t.TempDir()
	store, err := storage.NewFileStorage(tempDir, 1024*1024)
	if err != nil {
		t.Fatalf("failed to create file storage: %v", err)
	}

	guard := mediaSvc.NewResourceGuard(1, 10*1024*1024)
	media := mediaSvc.NewService(nil, store, guard)
	app := &App{media: media}
	app.lifecycleState.Store(uint32(lifecycleRunning))

	blockCh := make(chan struct{})
	readStarted := make(chan struct{})
	putDone := make(chan struct{})

	reader := &testBlockingReader{
		data:        []byte("active upload payload"),
		blockCh:     blockCh,
		readStarted: readStarted,
	}

	go func() {
		defer close(putDone)
		_, _ = store.Put(context.Background(), reader, storage.Metadata{Name: "upload.bin"})
	}()

	// 1. Explicit deterministic synchronization: wait until Put() enters Read() while holding transaction mutex
	select {
	case <-readStarted:
	case <-time.After(5 * time.Second):
		close(blockCh)
		t.Fatal("Put did not enter Read within deadline")
	}

	// 2. Measure actual duration under a watchdog to prevent hangs
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	type healthResult struct {
		snapshot HealthSnapshot
		duration time.Duration
	}
	resCh := make(chan healthResult, 1)

	go func() {
		start := time.Now()
		h := app.HealthContext(ctx)
		resCh <- healthResult{snapshot: h, duration: time.Since(start)}
	}()

	var h HealthSnapshot
	var elapsed time.Duration

	select {
	case res := <-resCh:
		h = res.snapshot
		elapsed = res.duration
	case <-time.After(500 * time.Millisecond):
		close(blockCh)
		t.Fatal("watchdog triggered: App.HealthContext blocked on storage transaction mutex!")
	}

	// 3. Assert intended time boundary (< 50ms, typically < 10ms for atomic snapshot + probe)
	if elapsed > 50*time.Millisecond {
		t.Errorf("App.HealthContext took %v, want <= 50ms", elapsed)
	}

	if h.Subsystems["storage"] != HealthHealthy {
		t.Fatalf("storage subsystem = %q, want healthy", h.Subsystems["storage"])
	}
	if h.Subsystems["filesystem"] != HealthHealthy {
		t.Fatalf("filesystem subsystem = %q, want healthy", h.Subsystems["filesystem"])
	}
	if !h.Ready {
		t.Fatal("app must be ready during non-blocking storage check")
	}

	// 4. Ensure Put goroutine terminates cleanly before test exit
	close(blockCh)
	select {
	case <-putDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Put goroutine did not finish after unblocking")
	}
}

type mockStorageWithHealth struct {
	storage.Storage
	healthStatus string
	healthReason string
}

func (m *mockStorageWithHealth) StorageHealth() (string, string) {
	return m.healthStatus, m.healthReason
}

func TestHealthContext_StorageDegradedNotOverwrittenByFilesystemHealthy(t *testing.T) {
	tempDir := t.TempDir()
	fileStore, err := storage.NewFileStorage(tempDir, 1024)
	if err != nil {
		t.Fatalf("failed to create file storage: %v", err)
	}

	mockStore := &mockStorageWithHealth{
		Storage:      fileStore,
		healthStatus: HealthDegraded,
		healthReason: "storage:quota-exceeded",
	}

	guard := mediaSvc.NewResourceGuard(1, 10*1024*1024)
	media := mediaSvc.NewService(nil, mockStore, guard)
	app := &App{media: media}
	app.lifecycleState.Store(uint32(lifecycleRunning))

	h := app.HealthContext(context.Background())

	// Subsystems must be independently reported:
	if h.Subsystems["storage"] != HealthDegraded {
		t.Fatalf("storage subsystem = %q, want degraded", h.Subsystems["storage"])
	}
	if h.Subsystems["filesystem"] != HealthHealthy {
		t.Fatalf("filesystem subsystem = %q, want healthy", h.Subsystems["filesystem"])
	}
	if !h.HasReason("storage:quota-exceeded") {
		t.Fatalf("missing storage:quota-exceeded reason: %v", h.Reasons)
	}
	if h.Status != HealthDegraded {
		t.Fatalf("app status = %q, want degraded (filesystem healthy must not overwrite storage degradation)", h.Status)
	}
}

type mockBasePathStorage struct {
	storage.Storage
	basePath string
}

func (m *mockBasePathStorage) BasePath() string { return m.basePath }

func TestHealthContext_StorageAndFilesystemCombinations(t *testing.T) {
	tempDir := t.TempDir()
	validDir := filepath.Join(tempDir, "valid")
	_ = os.MkdirAll(validDir, 0700)
	missingDir := filepath.Join(tempDir, "missing")

	tests := []struct {
		name           string
		basePath       string
		storageStatus  string
		storageReason  string
		wantStorage    string
		wantFilesystem string
		wantReason     string
		wantStatus     string
	}{
		{
			name:           "both healthy",
			basePath:       validDir,
			storageStatus:  HealthHealthy,
			wantStorage:    HealthHealthy,
			wantFilesystem: HealthHealthy,
			wantStatus:     HealthHealthy,
		},
		{
			name:           "storage degraded, filesystem healthy",
			basePath:       validDir,
			storageStatus:  HealthDegraded,
			storageReason:  "storage:quota-exceeded",
			wantStorage:    HealthDegraded,
			wantFilesystem: HealthHealthy,
			wantReason:     "storage:quota-exceeded",
			wantStatus:     HealthDegraded,
		},
		{
			name:           "storage healthy, filesystem missing",
			basePath:       missingDir,
			storageStatus:  HealthHealthy,
			wantStorage:    HealthHealthy,
			wantFilesystem: HealthUnhealthy,
			wantReason:     "filesystem:unavailable",
			wantStatus:     HealthDegraded,
		},
		{
			name:           "storage degraded, filesystem missing",
			basePath:       missingDir,
			storageStatus:  HealthDegraded,
			storageReason:  "storage:quota-exceeded",
			wantStorage:    HealthDegraded,
			wantFilesystem: HealthUnhealthy,
			wantReason:     "storage:quota-exceeded",
			wantStatus:     HealthDegraded,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &mockStorageWithHealth{
				Storage:      &mockBasePathStorage{basePath: tc.basePath},
				healthStatus: tc.storageStatus,
				healthReason: tc.storageReason,
			}
			guard := mediaSvc.NewResourceGuard(1, 10*1024*1024)
			media := mediaSvc.NewService(nil, store, guard)
			app := &App{media: media}
			app.lifecycleState.Store(uint32(lifecycleRunning))

			h := app.HealthContext(context.Background())
			if h.Subsystems["storage"] != tc.wantStorage {
				t.Errorf("storage subsystem = %q, want %q", h.Subsystems["storage"], tc.wantStorage)
			}
			if h.Subsystems["filesystem"] != tc.wantFilesystem {
				t.Errorf("filesystem subsystem = %q, want %q", h.Subsystems["filesystem"], tc.wantFilesystem)
			}
			if tc.wantReason != "" && !h.HasReason(tc.wantReason) {
				t.Errorf("missing expected reason %q in %v", tc.wantReason, h.Reasons)
			}
			if h.Status != tc.wantStatus {
				t.Errorf("app status = %q, want %q", h.Status, tc.wantStatus)
			}
		})
	}
}
