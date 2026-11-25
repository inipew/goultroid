package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/runtime"
)

type quiesceTrackingComp struct {
	mockResourceComp
	quiesceCalls int
}

func (q *quiesceTrackingComp) Quiesce(ctx context.Context) error {
	q.quiesceCalls++
	return nil
}

func TestAppShutdown_QuiescesThroughRuntimeWithoutDuplicateInvocation(t *testing.T) {
	rt := runtime.New()
	comp := &quiesceTrackingComp{
		mockResourceComp: mockResourceComp{name: "tracked-comp"},
	}
	_ = rt.Register(comp)

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("runtime Start failed: %v", err)
	}

	logger, _, _ := initLogger("error")
	a := &App{
		logger:       logger,
		runtime:      rt,
		shutdownDone: make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := a.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	if comp.quiesceCalls != 1 {
		t.Fatalf("expected component to be quiesced exactly once, got %d", comp.quiesceCalls)
	}
	if a.LifecycleState() != "stopped" {
		t.Fatalf("expected state stopped, got %s", a.LifecycleState())
	}
}

func TestAppShutdown_DetectsResourceLeak(t *testing.T) {
	rt := runtime.New()
	resMgr := resource.NewManager()
	_ = resMgr.Register(resource.Resource{
		ID:    "leaked-goroutine-1",
		Owner: "plugin:orphan",
		Type:  resource.TypeGoroutine,
	})

	logger, _, _ := initLogger("error")
	a := &App{
		logger:       logger,
		runtime:      rt,
		resources:    resMgr,
		shutdownDone: make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := a.Shutdown(ctx)
	if err == nil {
		t.Fatal("expected Shutdown to return resource leak error, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "resource leaks detected after shutdown") {
		t.Errorf("expected 'resource leaks detected after shutdown' in error, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "plugin:orphan:[goroutine=1]") {
		t.Errorf("expected 'plugin:orphan:[goroutine=1]' in error, got: %s", errMsg)
	}
	if a.LifecycleState() != "stopped" {
		t.Fatalf("expected state stopped, got %s", a.LifecycleState())
	}
}

func TestAppShutdown_ForceCleansEligibleResources(t *testing.T) {
	rt := runtime.New()
	resMgr := resource.NewManager()
	cleaned := false
	_ = resMgr.RegisterWithCleanup(resource.Resource{
		ID:    "temp-file-1",
		Owner: "plugin:downloader",
		Type:  resource.TypeTempFile,
	}, func() error {
		cleaned = true
		return nil
	})

	logger, _, _ := initLogger("error")
	a := &App{
		logger:       logger,
		runtime:      rt,
		resources:    resMgr,
		shutdownDone: make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := a.Shutdown(ctx); err != nil {
		t.Fatalf("expected clean shutdown after force cleanup, got: %v", err)
	}
	if !cleaned {
		t.Fatal("expected cleanup function to have been invoked")
	}
	if len(resMgr.All()) != 0 {
		t.Fatalf("expected 0 resources remaining, got %d", len(resMgr.All()))
	}
}

type mockFailingStopComp struct {
	name    string
	stopErr error
}

func (m *mockFailingStopComp) Name() string                { return m.name }
func (m *mockFailingStopComp) Dependencies() []string      { return nil }
func (m *mockFailingStopComp) Start(context.Context) error { return nil }
func (m *mockFailingStopComp) Stop(context.Context) error  { return m.stopErr }
func (m *mockFailingStopComp) Health(context.Context) runtime.ComponentHealth {
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}

func TestAppShutdown_CombinesRuntimeCleanupAndLeakErrors(t *testing.T) {
	rt := runtime.New()
	failingComp := &mockFailingStopComp{
		name:    "failing-comp",
		stopErr: errors.New("runtime component stop failed"),
	}
	_ = rt.Register(failingComp)
	_ = rt.Start(context.Background())

	resMgr := resource.NewManager()
	_ = resMgr.RegisterWithCleanup(resource.Resource{
		ID:    "failing-res",
		Owner: "plugin:broken",
		Type:  resource.TypeProcess,
	}, func() error {
		return errors.New("process kill failed")
	})

	logger, _, _ := initLogger("error")
	a := &App{
		logger:       logger,
		runtime:      rt,
		resources:    resMgr,
		shutdownDone: make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := a.Shutdown(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "runtime component stop failed") {
		t.Errorf("expected runtime error in shutdownErr, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "resource cleanup: cleanup failing-res: process kill failed") {
		t.Errorf("expected cleanup error in shutdownErr, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "resource leaks detected after shutdown: 1 unreleased resource(s)") {
		t.Errorf("expected leak error in shutdownErr, got: %s", errMsg)
	}
}
