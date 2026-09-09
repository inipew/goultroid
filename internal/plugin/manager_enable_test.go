package plugin

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/platform/audit"
	"github.com/inipew/goultroid/internal/runtime"
)

type failingManifestPlugin struct{}

func (*failingManifestPlugin) Name() string { return "failing-manifest" }
func (*failingManifestPlugin) Manifest() Manifest {
	return Manifest{ID: "failing-manifest", Name: "Failing", Version: "1.0.0", Capabilities: []string{CapSecretRead}}
}
func (*failingManifestPlugin) Init() error              { return errors.New("init failed") }
func (*failingManifestPlugin) Commands() []core.Command { return nil }

type blockingEnablePlugin struct {
	initCalls atomic.Int32
	block     atomic.Bool
	entered   chan struct{}
	release   chan struct{}
}

func (*blockingEnablePlugin) Name() string { return "blocking-enable" }
func (p *blockingEnablePlugin) Init() error {
	p.initCalls.Add(1)
	if p.block.Load() {
		close(p.entered)
		<-p.release
	}
	return nil
}
func (*blockingEnablePlugin) Commands() []core.Command { return nil }

type failedDisablePlugin struct{}

func (*failedDisablePlugin) Name() string             { return "failed-disable" }
func (*failedDisablePlugin) Init() error              { return nil }
func (*failedDisablePlugin) Commands() []core.Command { return nil }
func (*failedDisablePlugin) Shutdown() error          { return errors.New("shutdown failed") }

type togglablePlugin struct {
	name            string
	initCount       int
	shutdownCount   int
	initedWithScope bool
}

func (p *togglablePlugin) Name() string { return p.name }
func (p *togglablePlugin) Init() error  { p.initCount++; return nil }
func (p *togglablePlugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:    p.name + "cmd",
			Handler: func(ctx *core.Context) error { return nil },
		},
	}
}
func (p *togglablePlugin) InitScope(ctx context.Context, scope *Scope) error {
	p.initCount++
	p.initedWithScope = true
	_ = scope.Defer(func() {
		p.shutdownCount++
	})
	return nil
}

func TestManager_EnableDisable(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)

	p := &togglablePlugin{name: "toggle"}
	ctx := context.Background()

	if err := mgr.RegisterWithContext(ctx, p); err != nil {
		t.Fatalf("register: %v", err)
	}

	if !mgr.IsEnabled("toggle") {
		t.Fatalf("expected toggle to be enabled initially")
	}
	if p.initCount != 1 {
		t.Fatalf("expected initCount=1, got %d", p.initCount)
	}

	// Verify command is resolvable in router
	cmd, ok := router.Find("togglecmd")
	if !ok || cmd.Name != "togglecmd" {
		t.Fatalf("expected command togglecmd in router")
	}

	// Disable
	if err := mgr.Disable(ctx, "toggle"); err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	if mgr.IsEnabled("toggle") {
		t.Fatalf("expected toggle to be disabled")
	}
	if p.shutdownCount != 1 {
		t.Fatalf("expected shutdownCount=1, got %d", p.shutdownCount)
	}

	// Command should be removed from router
	_, ok = router.Find("togglecmd")
	if ok {
		t.Fatalf("expected command togglecmd to be removed from router")
	}

	disabledList := mgr.DisabledPlugins()
	if len(disabledList) != 1 || disabledList[0] != "toggle" {
		t.Fatalf("expected ['toggle'] in disabled list, got %v", disabledList)
	}

	// Disable again is idempotent
	if err := mgr.Disable(ctx, "toggle"); err != nil {
		t.Fatalf("second disable failed: %v", err)
	}

	// Enable
	if err := mgr.Enable(ctx, "toggle"); err != nil {
		t.Fatalf("enable failed: %v", err)
	}
	if !mgr.IsEnabled("toggle") {
		t.Fatalf("expected toggle to be enabled after Enable()")
	}
	if p.initCount != 2 {
		t.Fatalf("expected initCount=2 after re-enable, got %d", p.initCount)
	}

	// Command should be restored in router
	cmd, ok = router.Find("togglecmd")
	if !ok || cmd.Name != "togglecmd" {
		t.Fatalf("expected command togglecmd to be restored in router")
	}

	if len(mgr.DisabledPlugins()) != 0 {
		t.Fatalf("expected empty disabled list after re-enable, got %v", mgr.DisabledPlugins())
	}
}

func TestManager_FailedRegistrationRollsBackManifestAndGate(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	gate := NewCapabilityGate()
	mgr.SetPlatformServices(gate, nil, nil, nil, nil, nil, nil)
	err := mgr.RegisterWithContext(context.Background(), &failingManifestPlugin{})
	if err == nil {
		t.Fatal("expected registration failure")
	}
	if _, ok := mgr.Manifest("failing-manifest"); ok {
		t.Fatal("failed plugin manifest remained visible")
	}
	if err := gate.Check("failing-manifest", CapSecretRead); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("capability remained registered after failure: %v", err)
	}
}

func TestManager_ConcurrentEnableInitializesOnce(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	p := &blockingEnablePlugin{entered: make(chan struct{}), release: make(chan struct{})}
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Disable(context.Background(), p.Name()); err != nil {
		t.Fatal(err)
	}
	p.block.Store(true)
	firstDone := make(chan error, 1)
	go func() { firstDone <- mgr.Enable(context.Background(), p.Name()) }()
	select {
	case <-p.entered:
	case <-time.After(time.Second):
		t.Fatal("first enable did not enter initialization")
	}
	if err := mgr.Enable(context.Background(), p.Name()); err == nil {
		t.Fatal("concurrent Enable() was not rejected")
	}
	close(p.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if got := p.initCalls.Load(); got != 2 {
		t.Fatalf("Init() calls = %d, want initial registration plus one enable", got)
	}
}

func TestManager_FailedDisableIsObservableAndCannotReenable(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	p := &failedDisablePlugin{}
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Disable(context.Background(), p.Name()); err == nil {
		t.Fatal("expected disable failure")
	}
	if mgr.IsEnabled(p.Name()) {
		t.Fatal("plugin remained logically enabled after failed teardown")
	}
	if health := mgr.Health(context.Background()); health.Status != runtime.HealthDegraded {
		t.Fatalf("Health() = %+v, want degraded", health)
	}
	if err := mgr.Enable(context.Background(), p.Name()); err == nil {
		t.Fatal("plugin with incomplete teardown was re-enabled")
	}
}

type mockSchedulerCleaner struct {
	mu            sync.Mutex
	cleanedOwners []string
}

func (m *mockSchedulerCleaner) UnregisterPeriodicTasksByOwner(owner string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanedOwners = append(m.cleanedOwners, owner)
	return 1
}

func TestManager_Disable_CleansSchedulerAndJobs(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)

	schedCleaner := &mockSchedulerCleaner{}
	mgr.SetSchedulerCleaner(schedCleaner)

	jobsMgr := jobs.NewManager(nil)
	err := jobsMgr.Register(jobs.Job{
		ID:    "job-toggle-1",
		Owner: "plugin:toggle",
		Run:   func(ctx context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("register job: %v", err)
	}

	mgr.SetPlatformServices(nil, nil, nil, nil, nil, nil, jobsMgr)

	p := &togglablePlugin{name: "toggle"}
	ctx := context.Background()

	if err := mgr.RegisterWithContext(ctx, p); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Now disable the plugin
	if err := mgr.Disable(ctx, "toggle"); err != nil {
		t.Fatalf("disable: %v", err)
	}

	// Verify scheduler cleaner was called for both key and plugin:key
	schedCleaner.mu.Lock()
	defer schedCleaner.mu.Unlock()
	hasKey := false
	hasPluginKey := false
	for _, o := range schedCleaner.cleanedOwners {
		if o == "toggle" {
			hasKey = true
		}
		if o == "plugin:toggle" {
			hasPluginKey = true
		}
	}
	if !hasKey || !hasPluginKey {
		t.Errorf("expected scheduler cleaner called for toggle and plugin:toggle, got %v", schedCleaner.cleanedOwners)
	}

	// Verify jobs manager had the job cancelled/removed
	_, found := jobsMgr.Get("job-toggle-1")
	if found {
		t.Errorf("expected job-toggle-1 to be cancelled and removed from jobs manager")
	}
}

func TestManager_Auditor(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)
	auditor := audit.NewService(nil, 10)
	mgr.SetAuditor(auditor)

	p := &togglablePlugin{name: "toggle"}
	ctx := context.Background()

	if err := mgr.RegisterWithContext(ctx, p); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Disable
	if err := mgr.Disable(ctx, "toggle"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	recent := auditor.Recent(5)
	if len(recent) != 1 || recent[0].Action != "plugin.disable" || recent[0].Target != "toggle" {
		t.Fatalf("expected plugin.disable event, got %+v", recent)
	}

	// Enable
	if err := mgr.Enable(ctx, "toggle"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	recent = auditor.Recent(5)
	if len(recent) != 2 || recent[0].Action != "plugin.enable" || recent[0].Target != "toggle" {
		t.Fatalf("expected plugin.enable event, got %+v", recent)
	}
}
