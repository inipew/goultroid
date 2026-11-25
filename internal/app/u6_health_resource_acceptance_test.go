package app

import (
	"context"
	"errors"
	"os"
	stdruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/services/storage"
)

type u6DegradedSecretComp struct {
	name string
	err  error
}

func (c *u6DegradedSecretComp) Name() string                { return c.name }
func (c *u6DegradedSecretComp) Dependencies() []string      { return nil }
func (c *u6DegradedSecretComp) IsCritical() bool            { return false }
func (c *u6DegradedSecretComp) Start(context.Context) error { return c.err }
func (c *u6DegradedSecretComp) Stop(context.Context) error  { return nil }
func (c *u6DegradedSecretComp) Health(context.Context) runtime.ComponentHealth {
	return runtime.ComponentHealth{
		Status:  runtime.HealthDegraded,
		Details: "https://api.telegram.org/bot12345:SECRET_TOKEN_XYZ/getMe?token=supersecret123",
	}
}

func TestU6_S08_ResourceBoundsLeakReportAndHealthSanitization(t *testing.T) {
	t.Run("capacity bound enforced", func(t *testing.T) {
		mgr := resource.NewManagerWithLimit(3)
		for i := 0; i < 3; i++ {
			if err := mgr.Register(resource.Resource{
				ID:    string(rune('a' + i)),
				Owner: "owner-1",
				Type:  resource.TypeTempFile,
			}); err != nil {
				t.Fatalf("Register %d: %v", i, err)
			}
		}
		if err := mgr.Register(resource.Resource{
			ID:    "overflow",
			Owner: "owner-1",
			Type:  resource.TypeTempFile,
		}); err == nil || !strings.Contains(err.Error(), "capacity reached") {
			t.Fatalf("expected resource capacity reached error, got: %v", err)
		}
	})

	t.Run("VerifyShutdownClean reports deterministic owner summary", func(t *testing.T) {
		mgr := resource.NewManager()
		_ = mgr.Register(resource.Resource{ID: "r1", Owner: "voice", Type: resource.TypeGoroutine})
		_ = mgr.Register(resource.Resource{ID: "r2", Owner: "voice", Type: resource.TypeGoroutine})
		_ = mgr.Register(resource.Resource{ID: "r3", Owner: "media", Type: resource.TypeTempFile})

		err := mgr.VerifyShutdownClean()
		if err == nil {
			t.Fatal("expected VerifyShutdownClean to report leaks")
		}
		if !strings.Contains(err.Error(), "media:[temp_file=1]; voice:[goroutine=2]") {
			t.Fatalf("unexpected VerifyShutdownClean summary: %v", err)
		}
	})

	t.Run("App.HealthContext sanitizes sensitive tokens and paths", func(t *testing.T) {
		rt := runtime.New()
		comp := &u6DegradedSecretComp{
			name: "plugin:/home/user/secret/dir?token=supersecret123",
			err:  errors.New("failed at https://example.com/path?token=supersecret123"),
		}
		if err := rt.Register(comp); err != nil {
			t.Fatalf("rt.Register: %v", err)
		}
		if err := rt.Start(context.Background()); err != nil {
			t.Fatalf("rt.Start: %v", err)
		}
		defer func() { _ = rt.Stop(context.Background()) }()

		resMgr := resource.NewManager()
		_ = resMgr.Register(resource.Resource{
			ID:    "leak-1",
			Owner: "owner:/home/private/key?token=supersecret123",
			Type:  resource.TypeTempFile,
		})
		_ = resMgr.DetectLeaks("owner:/home/private/key?token=supersecret123")

		a := &App{
			runtime:      rt,
			resources:    resMgr,
			shutdownDone: make(chan struct{}),
		}
		a.lifecycleState.Store(uint32(lifecycleRunning))

		health := a.HealthContext(context.Background())
		if health.Status != HealthDegraded {
			t.Fatalf("health status = %q, want degraded", health.Status)
		}
		joined := strings.Join(health.Reasons, "|")
		if strings.Contains(joined, "supersecret123") || strings.Contains(joined, "SECRET_TOKEN_XYZ") || strings.Contains(joined, "/home/private") {
			t.Fatalf("HealthContext leaked sensitive string in reasons: %v", health.Reasons)
		}
		const wantLeakReason = "resources:leak:owner-home-private-key"
		if !slicesContains(health.Reasons, wantLeakReason) {
			t.Fatalf("expected exact sanitized leak reason %q in %v", wantLeakReason, health.Reasons)
		}
		for _, reason := range health.Reasons {
			parts := strings.Split(reason, ":")
			last := parts[len(parts)-1]
			if len(last) > 48 {
				t.Fatalf("sanitized identifier exceeded 48 chars: %q", last)
			}
		}
	})
}

func slicesContains(items []string, target string) bool {
	for _, it := range items {
		if it == target {
			return true
		}
	}
	return false
}

func TestU6_S12_HealthFilesystemProbeConcurrencySaturation(t *testing.T) {
	gate := newU6ExecutionGate()
	var statCalls atomic.Int32

	restore := setFSProbeStatFn(func(path string) (os.FileInfo, error) {
		statCalls.Add(1)
		gate.MarkStarted()
		<-gate.releaseCh
		return nil, nil
	})
	defer restore()

	store, err := storage.NewFileStorage(t.TempDir(), 1024*1024)
	if err != nil {
		t.Fatalf("storage.NewFileStorage: %v", err)
	}

	a := &App{
		storage:      store,
		shutdownDone: make(chan struct{}),
	}
	a.lifecycleState.Store(uint32(lifecycleRunning))

	// First caller enters fsProbeSlot and blocks on gate.
	firstDone := make(chan HealthSnapshot, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		firstDone <- a.HealthContext(ctx)
	}()

	if err := gate.WaitStarted(context.Background()); err != nil {
		t.Fatalf("WaitStarted for initial fs probe: %v", err)
	}

	// Burst of 50 concurrent callers while probe slot is occupied.
	const burst = 50
	var wg sync.WaitGroup
	wg.Add(burst)
	saturatedCount := atomic.Int32{}
	for i := 0; i < burst; i++ {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			snap := a.HealthContext(ctx)
			if snap.HasReason("filesystem:probe-saturated") {
				saturatedCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := statCalls.Load(); got != 1 {
		gate.Release()
		t.Fatalf("expected exactly 1 active filesystem stat call, got %d", got)
	}
	if got := saturatedCount.Load(); got != burst {
		gate.Release()
		t.Fatalf("expected all %d concurrent callers to observe filesystem:probe-saturated, got %d", burst, got)
	}

	firstSnap := <-firstDone
	if !firstSnap.HasReason("filesystem:probe-timeout") {
		gate.Release()
		t.Fatalf("expected first probe to report filesystem:probe-timeout, got %v", firstSnap.Reasons)
	}

	// Release probe barrier and confirm slot becomes usable again and executes stat().
	gate.Release()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		snap := a.HealthContext(ctx)
		cancel()
		if snap.Subsystems["filesystem"] == HealthHealthy {
			if got := statCalls.Load(); got < 2 {
				t.Fatalf("expected recovery probe to execute stat() (calls >= 2), got %d", got)
			}
			return
		}
		stdruntime.Gosched()
	}
	t.Fatal("filesystem probe slot did not recover after barrier release")
}
