package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/voice"
)

// waitForGoroutinesSettled waits deterministically until goroutine count returns
// to within baseline+tolerance, or deadline expires.
func waitForGoroutinesSettled(t *testing.T, baseline, tolerance int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current := stdruntime.NumGoroutine()
		if current <= baseline+tolerance {
			return
		}
		stdruntime.Gosched()
		time.Sleep(5 * time.Millisecond)
	}
	current := stdruntime.NumGoroutine()
	if current > baseline+tolerance {
		t.Fatalf("goroutines did not settle to baseline: baseline=%d current=%d tolerance=%d", baseline, current, tolerance)
	}
}

// supervisorComp wraps a runtime.Supervisor as a runtime.Component for App testing.
type supervisorComp struct {
	name       string
	supervisor *runtime.Supervisor
}

func (s *supervisorComp) Name() string           { return s.name }
func (s *supervisorComp) Dependencies() []string { return nil }
func (s *supervisorComp) Start(ctx context.Context) error {
	return s.supervisor.Start(ctx)
}
func (s *supervisorComp) Stop(ctx context.Context) error {
	return s.supervisor.Stop(ctx)
}
func (s *supervisorComp) ForceStop(ctx context.Context) error {
	return s.supervisor.ForceStop(ctx)
}
func (s *supervisorComp) Health(ctx context.Context) runtime.ComponentHealth {
	return s.supervisor.Health(ctx)
}

func TestRepeatedLifecycle_StartStopRounds(t *testing.T) {
	baselineG := stdruntime.NumGoroutine()
	const rounds = 5

	for round := 0; round < rounds; round++ {
		rt := runtime.New()
		resMgr := resource.NewManager()
		sup := runtime.NewSupervisor(runtime.WithMaxWorkers(10), runtime.WithBaseBackoff(20*time.Millisecond))

		workerStarted := make(chan struct{})
		err := sup.Register(runtime.WorkerSpec{
			Name:    fmt.Sprintf("worker-round-%d", round),
			Restart: runtime.RestartTransient,
			Run: func(ctx context.Context) error {
				close(workerStarted)
				<-ctx.Done()
				return nil
			},
		})
		if err != nil {
			t.Fatalf("round %d register worker: %v", round, err)
		}

		// Register a tracked resource with cleanup
		cleaned := false
		err = resMgr.RegisterWithCleanup(resource.Resource{
			ID:    fmt.Sprintf("res-round-%d", round),
			Owner: "test-owner",
			Type:  resource.TypeTempFile,
		}, func() error {
			cleaned = true
			return nil
		})
		if err != nil {
			t.Fatalf("round %d register resource: %v", round, err)
		}

		sComp := &supervisorComp{name: "supervisor", supervisor: sup}
		if err := rt.Register(sComp); err != nil {
			t.Fatalf("round %d register supervisor comp: %v", round, err)
		}

		if err := rt.Start(context.Background()); err != nil {
			t.Fatalf("round %d rt.Start failed: %v", round, err)
		}

		// Wait deterministically for worker to start
		select {
		case <-workerStarted:
		case <-time.After(2 * time.Second):
			t.Fatalf("round %d worker did not start in time", round)
		}

		logger, _, _ := initLogger("error")
		a := &App{
			logger:       logger,
			runtime:      rt,
			resources:    resMgr,
			shutdownDone: make(chan struct{}),
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := a.Shutdown(shutdownCtx); err != nil {
			cancel()
			t.Fatalf("round %d shutdown failed: %v", round, err)
		}
		cancel()

		if !cleaned {
			t.Fatalf("round %d resource was not cleaned", round)
		}
		if err := resMgr.VerifyShutdownClean(); err != nil {
			t.Fatalf("round %d verify shutdown clean: %v", round, err)
		}
		if sup.State() != runtime.StateStopped {
			t.Fatalf("round %d supervisor state = %v, want StateStopped", round, sup.State())
		}
	}

	// Verify goroutines settle back to baseline after all rounds
	waitForGoroutinesSettled(t, baselineG, 3, 2*time.Second)
}

func TestRepeatedLifecycle_WorkerPanicAndRestart(t *testing.T) {
	baselineG := stdruntime.NumGoroutine()
	const rounds = 3

	for round := 0; round < rounds; round++ {
		rt := runtime.New()
		resMgr := resource.NewManager()
		sup := runtime.NewSupervisor(runtime.WithMaxWorkers(10), runtime.WithBaseBackoff(10*time.Millisecond))

		var attempts atomic.Int32
		restarted := make(chan struct{})

		err := sup.Register(runtime.WorkerSpec{
			Name:          fmt.Sprintf("panic-worker-%d", round),
			Restart:       runtime.RestartTransient,
			MaxRestarts:   5,
			RestartWindow: time.Minute,
			Run: func(ctx context.Context) error {
				n := attempts.Add(1)
				if n == 1 {
					panic("simulated worker panic")
				}
				if n == 2 {
					close(restarted)
				}
				<-ctx.Done()
				return nil
			},
		})
		if err != nil {
			t.Fatalf("round %d register worker: %v", round, err)
		}

		sComp := &supervisorComp{name: "supervisor", supervisor: sup}
		_ = rt.Register(sComp)

		if err := rt.Start(context.Background()); err != nil {
			t.Fatalf("round %d rt.Start failed: %v", round, err)
		}

		select {
		case <-restarted:
		case <-time.After(2 * time.Second):
			t.Fatalf("round %d worker did not restart after panic", round)
		}

		logger, _, _ := initLogger("error")
		a := &App{
			logger:       logger,
			runtime:      rt,
			resources:    resMgr,
			shutdownDone: make(chan struct{}),
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := a.Shutdown(shutdownCtx); err != nil {
			cancel()
			t.Fatalf("round %d shutdown failed: %v", round, err)
		}
		cancel()

		if err := resMgr.VerifyShutdownClean(); err != nil {
			t.Fatalf("round %d verify shutdown clean: %v", round, err)
		}
	}

	waitForGoroutinesSettled(t, baselineG, 3, 2*time.Second)
}

func TestRepeatedLifecycle_CancellationWhileWorkerActive(t *testing.T) {
	baselineG := stdruntime.NumGoroutine()

	rt := runtime.New()
	resMgr := resource.NewManager()
	sup := runtime.NewSupervisor(runtime.WithMaxWorkers(10), runtime.WithBaseBackoff(20*time.Millisecond))

	active := make(chan struct{})
	exited := make(chan struct{})

	_ = sup.Register(runtime.WorkerSpec{
		Name:    "blocking-worker",
		Restart: runtime.RestartTransient,
		Run: func(ctx context.Context) error {
			close(active)
			<-ctx.Done()
			close(exited)
			return nil
		},
	})

	sComp := &supervisorComp{name: "supervisor", supervisor: sup}
	_ = rt.Register(sComp)
	_ = rt.Start(context.Background())

	select {
	case <-active:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not become active")
	}

	logger, _, _ := initLogger("error")
	a := &App{
		logger:       logger,
		runtime:      rt,
		resources:    resMgr,
		shutdownDone: make(chan struct{}),
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := a.Shutdown(shutdownCtx); err != nil {
		cancel()
		t.Fatalf("shutdown failed: %v", err)
	}
	cancel()

	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("worker did not exit upon shutdown cancellation")
	}

	if sup.State() != runtime.StateStopped {
		t.Fatalf("supervisor state = %v, want StateStopped", sup.State())
	}

	waitForGoroutinesSettled(t, baselineG, 3, 2*time.Second)
}

func TestRepeatedLifecycle_ForceStopUncooperativeWorker(t *testing.T) {
	baselineG := stdruntime.NumGoroutine()

	sup := runtime.NewSupervisor(runtime.WithMaxWorkers(10), runtime.WithBaseBackoff(10*time.Millisecond))
	workerRelease := make(chan struct{})
	workerStarted := make(chan struct{})

	_ = sup.Register(runtime.WorkerSpec{
		Name:    "uncooperative-worker",
		Restart: runtime.NeverRestart,
		Run: func(ctx context.Context) error {
			close(workerStarted)
			// Ignore ctx.Done() until explicit release barrier
			<-workerRelease
			return nil
		},
	})

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("sup.Start failed: %v", err)
	}

	select {
	case <-workerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}

	// ForceStop with very short timeout
	forceCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	err := sup.ForceStop(forceCtx)
	cancel()

	if err == nil {
		t.Fatal("expected ForceStop to time out for uncooperative worker, got nil")
	}
	// Supervisor must NOT falsely claim StateStopped
	if sup.State() == runtime.StateStopped {
		t.Fatal("supervisor falsely reported StateStopped while worker is still running")
	}
	if sup.State() != runtime.StateStopping {
		t.Fatalf("supervisor state = %v, want StateStopping", sup.State())
	}

	// Release the worker barrier
	close(workerRelease)

	// Supervisor background join reaper must now reap worker and transition to StateStopped
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sup.State() == runtime.StateStopped {
			break
		}
		stdruntime.Gosched()
		time.Sleep(5 * time.Millisecond)
	}
	if sup.State() != runtime.StateStopped {
		t.Fatalf("supervisor did not transition to StateStopped after worker exited: state=%v", sup.State())
	}

	waitForGoroutinesSettled(t, baselineG, 3, 2*time.Second)
}

type mockVoiceRepoStub struct{}

func (m *mockVoiceRepoStub) AddVoiceQueueTrack(ctx context.Context, track *voice.VoiceQueueRecord) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}
func (m *mockVoiceRepoStub) PopVoiceQueueTrack(ctx context.Context, chatID int64) (*voice.VoiceQueueRecord, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return &voice.VoiceQueueRecord{Title: "Track", SourceURL: "http://example.com/audio.mp3"}, nil
}
func (m *mockVoiceRepoStub) ClearVoiceQueue(ctx context.Context, chatID int64) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}
func (m *mockVoiceRepoStub) GetVoiceQueue(ctx context.Context, chatID int64) ([]*voice.VoiceQueueRecord, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
func (m *mockVoiceRepoStub) UpsertVoiceSession(ctx context.Context, rec *voice.VoiceSessionRecord) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}
func (m *mockVoiceRepoStub) GetVoiceSession(ctx context.Context, chatID int64) (*voice.VoiceSessionRecord, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
func (m *mockVoiceRepoStub) DeleteVoiceQueueTrack(ctx context.Context, id int64) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func TestRepeatedLifecycle_VoicePersistenceLifecycle(t *testing.T) {
	baselineG := stdruntime.NumGoroutine()
	repo := &mockVoiceRepoStub{}

	const rounds = 5
	for round := 0; round < rounds; round++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

		q, err := voice.NewQueue(ctx, 12345, repo)
		if err != nil {
			cancel()
			t.Fatalf("round %d NewQueue: %v", round, err)
		}
		track := voice.Source{Title: "Track", SourceURL: "http://example.com/audio.mp3"}
		if err := q.Enqueue(ctx, track); err != nil {
			cancel()
			t.Fatalf("round %d Enqueue: %v", round, err)
		}
		if q.Len() != 1 {
			cancel()
			t.Fatalf("round %d q.Len() = %d, want 1", round, q.Len())
		}

		popped, ok, err := q.Dequeue(ctx)
		if err != nil || !ok || popped.Title != "Track" {
			cancel()
			t.Fatalf("round %d Dequeue: (%v, %v, %v)", round, popped, ok, err)
		}

		if err := q.Clear(ctx); err != nil {
			cancel()
			t.Fatalf("round %d Clear: %v", round, err)
		}

		// Test cancellation bounds
		canceledCtx, cancelImmediate := context.WithCancel(context.Background())
		cancelImmediate()
		if err := q.Enqueue(canceledCtx, track); !errors.Is(err, context.Canceled) {
			cancel()
			t.Fatalf("round %d expected context.Canceled on Enqueue with canceled ctx, got %v", round, err)
		}

		svc := voice.NewService(voice.NewMockBackend(), repo, nil, nil)
		if _, err := svc.GetOrCreateSession(ctx, 12345); err != nil {
			cancel()
			t.Fatalf("round %d GetOrCreateSession: %v", round, err)
		}
		if err := svc.SetVolume(ctx, 12345, 80); err != nil {
			cancel()
			t.Fatalf("round %d SetVolume: %v", round, err)
		}
		if err := svc.SetRepeatMode(ctx, 12345, voice.RepeatTrack); err != nil {
			cancel()
			t.Fatalf("round %d SetRepeatMode: %v", round, err)
		}
		if err := svc.Shutdown(ctx); err != nil {
			cancel()
			t.Fatalf("round %d Shutdown: %v", round, err)
		}
		cancel()
	}

	waitForGoroutinesSettled(t, baselineG, 3, 2*time.Second)
}

func TestRepeatedLifecycle_RealAppCompositionStartShutdownRounds(t *testing.T) {
	baselineG := stdruntime.NumGoroutine()
	const rounds = 3

	for round := 0; round < rounds; round++ {
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
			t.Fatalf("round %d New(cfg) failed: %v", round, err)
		}

		startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := app.startBackgroundServices(startCtx); err != nil {
			startCancel()
			t.Fatalf("round %d startBackgroundServices failed: %v", round, err)
		}
		startCancel()

		// Register a managed worker in the real App supervisor
		workerStarted := make(chan struct{})
		err = app.supervisor.Register(runtime.WorkerSpec{
			Name:    fmt.Sprintf("composition-worker-%d", round),
			Restart: runtime.NeverRestart,
			Run: func(ctx context.Context) error {
				close(workerStarted)
				<-ctx.Done()
				return nil
			},
		})
		if err != nil {
			t.Fatalf("round %d supervisor.Register: %v", round, err)
		}

		select {
		case <-workerStarted:
		case <-time.After(2 * time.Second):
			t.Fatalf("round %d worker did not start", round)
		}

		// Register a tracked resource in the real App resource manager
		cleaned := false
		err = app.resources.RegisterWithCleanup(resource.Resource{
			ID:    fmt.Sprintf("comp-res-%d", round),
			Owner: "test-plugin",
			Type:  resource.TypeTempFile,
		}, func() error {
			cleaned = true
			return nil
		})
		if err != nil {
			t.Fatalf("round %d resources.Register: %v", round, err)
		}

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = app.Shutdown(shutdownCtx)
		shutdownCancel()
		if err != nil {
			t.Fatalf("round %d Shutdown: %v", round, err)
		}

		if !cleaned {
			t.Fatalf("round %d expected resource to be cleaned", round)
		}
		if err := app.resources.VerifyShutdownClean(); err != nil {
			t.Fatalf("round %d VerifyShutdownClean: %v", round, err)
		}
		if app.LifecycleState() != "stopped" {
			t.Fatalf("round %d expected state stopped, got %s", round, app.LifecycleState())
		}
	}

	waitForGoroutinesSettled(t, baselineG, 5, 3*time.Second)
}

func TestRepeatedLifecycle_LateCleanupCallbackDetected(t *testing.T) {
	rt := runtime.New()
	resMgr := resource.NewManager()
	blockCleanup := make(chan struct{})
	cleanupStarted := make(chan struct{})

	_ = resMgr.RegisterWithCleanup(resource.Resource{
		ID:    "blocked-res",
		Owner: "plugin:slow",
		Type:  resource.TypeTempFile,
	}, func() error {
		close(cleanupStarted)
		<-blockCleanup
		return nil
	})

	logger, _, _ := initLogger("error")
	a := &App{
		logger:       logger,
		runtime:      rt,
		resources:    resMgr,
		shutdownDone: make(chan struct{}),
	}

	shortCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	err := a.Shutdown(shortCtx)
	if err == nil {
		t.Fatal("expected error from Shutdown when cleanup callback is delayed, got nil")
	}

	// Release the blocked cleanup function
	close(blockCleanup)

	errMsg := err.Error()
	if !strings.Contains(errMsg, "deadline") && !strings.Contains(errMsg, "resource leaks detected after shutdown") {
		t.Fatalf("expected deadline or resource leak error in shutdownErr, got: %s", errMsg)
	}
}

type instrumentedVoiceRepo struct {
	activeOps atomic.Int32
}

func (m *instrumentedVoiceRepo) AddVoiceQueueTrack(ctx context.Context, track *voice.VoiceQueueRecord) error {
	m.activeOps.Add(1)
	defer m.activeOps.Add(-1)
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}
func (m *instrumentedVoiceRepo) PopVoiceQueueTrack(ctx context.Context, chatID int64) (*voice.VoiceQueueRecord, error) {
	m.activeOps.Add(1)
	defer m.activeOps.Add(-1)
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return &voice.VoiceQueueRecord{Title: "T"}, nil
}
func (m *instrumentedVoiceRepo) ClearVoiceQueue(ctx context.Context, chatID int64) error {
	m.activeOps.Add(1)
	defer m.activeOps.Add(-1)
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}
func (m *instrumentedVoiceRepo) GetVoiceQueue(ctx context.Context, chatID int64) ([]*voice.VoiceQueueRecord, error) {
	m.activeOps.Add(1)
	defer m.activeOps.Add(-1)
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
func (m *instrumentedVoiceRepo) UpsertVoiceSession(ctx context.Context, rec *voice.VoiceSessionRecord) error {
	m.activeOps.Add(1)
	defer m.activeOps.Add(-1)
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}
func (m *instrumentedVoiceRepo) GetVoiceSession(ctx context.Context, chatID int64) (*voice.VoiceSessionRecord, error) {
	m.activeOps.Add(1)
	defer m.activeOps.Add(-1)
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
func (m *instrumentedVoiceRepo) DeleteVoiceQueueTrack(ctx context.Context, id int64) error {
	m.activeOps.Add(1)
	defer m.activeOps.Add(-1)
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func TestRepeatedLifecycle_VoiceShutdownNoLingeringOperations(t *testing.T) {
	repo := &instrumentedVoiceRepo{}
	mockBackend := voice.NewMockBackend()
	svc := voice.NewService(mockBackend, repo, nil, nil)

	ctx := context.Background()
	_, _, err := svc.Play(ctx, -1001, voice.Source{Title: "T1", ChatID: -1001})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = svc.Play(ctx, -1002, voice.Source{Title: "T2", ChatID: -1002})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Active database operations must be 0 immediately upon return of Shutdown
	if active := repo.activeOps.Load(); active != 0 {
		t.Fatalf("expected 0 active database operations after shutdown, got %d", active)
	}

	// Operations after shutdown must fail closed immediately without initiating new DB ops
	if _, _, err := svc.Play(ctx, -1003, voice.Source{Title: "T3", ChatID: -1003}); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped on Play after shutdown, got %v", err)
	}
	if active := repo.activeOps.Load(); active != 0 {
		t.Fatalf("expected 0 DB operations after rejected post-shutdown call, got %d", active)
	}
}

type blockingMockVoiceBackend struct {
	*voice.MockBackend
	enteredPlay chan struct{}
	barrierPlay chan struct{}
}

func (b *blockingMockVoiceBackend) Play(ctx context.Context, chatID int64, source voice.Source) error {
	if b.enteredPlay != nil {
		select {
		case b.enteredPlay <- struct{}{}:
		default:
		}
	}
	if b.barrierPlay != nil {
		select {
		case <-b.barrierPlay:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.MockBackend.Play(ctx, chatID, source)
}

func TestRepeatedLifecycle_VoiceShutdownConcurrentInFlightOperationCoordination(t *testing.T) {
	repo := &instrumentedVoiceRepo{}
	entered := make(chan struct{})
	barrier := make(chan struct{})
	backend := &blockingMockVoiceBackend{
		MockBackend: voice.NewMockBackend(),
		enteredPlay: entered,
		barrierPlay: barrier,
	}
	svc := voice.NewService(backend, repo, nil, nil)
	ctx := context.Background()
	chatID := int64(-2001)

	playErrCh := make(chan error, 1)
	go func() {
		_, _, err := svc.Play(ctx, chatID, voice.Source{Title: "InFlight", ChatID: chatID})
		playErrCh <- err
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for in-flight Play to enter barrier")
	}

	shutdownErrCh := make(chan error, 1)
	go func() {
		shutdownErrCh <- svc.Shutdown(ctx)
	}()

	select {
	case err := <-shutdownErrCh:
		t.Fatalf("Shutdown returned before in-flight operation finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Verify new operations reject immediately
	if _, _, err := svc.Play(ctx, -2002, voice.Source{Title: "Rejected", ChatID: -2002}); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped on new Play during shutdown, got %v", err)
	}

	// Release barrier
	close(barrier)

	select {
	case err := <-playErrCh:
		if err != nil {
			t.Fatalf("in-flight Play failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Play to finish")
	}

	select {
	case err := <-shutdownErrCh:
		if err != nil {
			t.Fatalf("Shutdown failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Shutdown to complete")
	}

	if active := repo.activeOps.Load(); active != 0 {
		t.Fatalf("expected 0 active database operations after shutdown, got %d", active)
	}
	if backend.IsActive(chatID) {
		t.Fatal("expected backend to be torn down after shutdown")
	}
}
