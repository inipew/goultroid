package plugin

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/runtime"
)

type lifecyclePlugin struct {
	name      string
	commands  []core.Command
	shutdowns atomic.Int32
	order     *[]string
}

func (p *lifecyclePlugin) Name() string { return p.name }
func (p *lifecyclePlugin) Commands() []core.Command {
	res := make([]core.Command, len(p.commands))
	for i, c := range p.commands {
		res[i] = c
		if res[i].Handler == nil {
			res[i].Handler = func(ctx *core.Context) error { return nil }
		}
	}
	return res
}
func (p *lifecyclePlugin) Init() error { return nil }
func (p *lifecyclePlugin) Shutdown() error {
	p.shutdowns.Add(1)
	if p.order != nil {
		*p.order = append(*p.order, p.name)
	}
	return nil
}

type contextLifecyclePlugin struct {
	lifecyclePlugin
	observedDeadline atomic.Bool
}

type blockingLifecyclePlugin struct {
	lifecyclePlugin
	started chan struct{}
	release chan struct{}
}

func (p *blockingLifecyclePlugin) Shutdown() error {
	close(p.started)
	<-p.release
	p.shutdowns.Add(1)
	return nil
}

func (p *contextLifecyclePlugin) ShutdownContext(ctx context.Context) error {
	if _, ok := ctx.Deadline(); ok {
		p.observedDeadline.Store(true)
	}
	p.shutdowns.Add(1)
	return ctx.Err()
}

func TestManager_RegisterIsAtomicOnCommandConflict(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)
	if err := mgr.Register(&lifecyclePlugin{name: "existing", commands: []core.Command{{Name: "taken"}}}); err != nil {
		t.Fatal(err)
	}

	broken := &lifecyclePlugin{name: "broken", commands: []core.Command{{Name: "free"}, {Name: "taken"}, {Name: "also-free"}}}
	if err := mgr.Register(broken); err == nil {
		t.Fatal("expected registration conflict")
	}
	if _, ok := router.Find("free"); ok {
		t.Fatal("partial command registration leaked into router")
	}
	if _, ok := router.Find("also-free"); ok {
		t.Fatal("partial command registration leaked into router")
	}
	if _, ok := mgr.Find("broken"); ok {
		t.Fatal("failed plugin was added to manager")
	}
	if broken.shutdowns.Load() != 1 {
		t.Fatalf("expected failed plugin cleanup once, got %d", broken.shutdowns.Load())
	}
}

func TestManager_ShutdownIsReverseOrderAndIdempotent(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)
	order := make([]string, 0, 3)
	for _, name := range []string{"first", "second", "third"} {
		if err := mgr.Register(&lifecyclePlugin{name: name, commands: []core.Command{{Name: name}}, order: &order}); err != nil {
			t.Fatal(err)
		}
	}
	if err := mgr.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Shutdown(); err != nil {
		t.Fatal(err)
	}
	want := []string{"third", "second", "first"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown order = %v, want %v", order, want)
	}
	if _, err := func() (Plugin, error) { p := &lifecyclePlugin{name: "late"}; return p, mgr.Register(p) }(); err == nil {
		t.Fatal("expected registration to be rejected after shutdown")
	}
	if _, ok := router.Find("first"); ok {
		t.Fatal("plugin command remained registered after shutdown")
	}
}

func TestManager_ContextShutdownerReceivesContext(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)
	p := &contextLifecyclePlugin{lifecyclePlugin: lifecyclePlugin{name: "ctx", commands: []core.Command{{Name: "ctx"}}}}
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := mgr.ShutdownWithContext(ctx); !errors.Is(err, context.DeadlineExceeded) && err != nil {
		t.Fatalf("unexpected shutdown error: %v", err)
	}
	if !p.observedDeadline.Load() {
		t.Fatal("context-aware plugin did not receive deadline context")
	}
	if p.shutdowns.Load() != 1 {
		t.Fatalf("expected one context shutdown, got %d", p.shutdowns.Load())
	}
}

func TestScopeCancelsWorkAndRunsCleanup(t *testing.T) {
	scope := NewScope(context.Background(), "plugin:test")
	started := make(chan struct{})
	finished := make(chan struct{})
	if err := scope.Go(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(finished)
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	cleaned := atomic.Bool{}
	if err := scope.Defer(func() { cleaned.Store(true) }); err != nil {
		t.Fatal(err)
	}
	if err := scope.Track(Resource{ID: "task-1", Type: "task"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scope.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("scoped goroutine did not stop")
	}
	if !cleaned.Load() {
		t.Fatal("scope cleanup did not run")
	}
	if len(scope.Resources()) != 1 {
		t.Fatal("resource snapshot unexpectedly changed before explicit release")
	}
}

func TestScopeWithManagerDetectsLeaks(t *testing.T) {
	mgr := resource.NewManager()
	scope := NewScopeWithManager(context.Background(), "plugin:leak_test", mgr)

	// Track a resource that is NOT released before scope Close
	if err := scope.Track(Resource{ID: "orphan-file", Type: resource.TypeTempFile}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := scope.Close(ctx)
	if err == nil {
		t.Fatal("expected Scope.Close to return an error when resources are leaked")
	}

	snap := mgr.OwnerSnapshot("plugin:leak_test")
	if snap.Leaked != 1 {
		t.Fatalf("expected 1 leaked resource in manager snapshot, got: %+v", snap)
	}
}

func TestScope_TrackWebSocket(t *testing.T) {
	mgr := resource.NewManager()
	scope := NewScopeWithManager(context.Background(), "plugin:ws_test", mgr)

	closedCalled := false
	err := scope.TrackWebSocket("conn-1", "wss://example.com/ws", func() error {
		closedCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error tracking websocket: %v", err)
	}

	snap := mgr.OwnerSnapshot("plugin:ws_test")
	if snap.TotalActive != 1 || snap.CountsByType[resource.TypeWebSocket] != 1 {
		t.Fatalf("expected 1 active websocket in manager snapshot, got: %+v", snap)
	}

	if err := scope.Close(context.Background()); err != nil {
		t.Fatalf("unexpected error closing scope: %v", err)
	}

	if !closedCalled {
		t.Fatalf("expected closeFn to be called upon scope close")
	}

	snapAfter := mgr.OwnerSnapshot("plugin:ws_test")
	if snapAfter.TotalActive != 0 || snapAfter.Leaked != 0 {
		t.Fatalf("expected 0 active/leaked resources after close, got: %+v", snapAfter)
	}
}

func TestScope_GoroutineBudgetEnforcement(t *testing.T) {
	scope := NewScope(context.Background(), "plugin:budget_test")
	scope.SetMaxGoroutines(2)

	release := make(chan struct{})
	started1 := make(chan struct{})
	started2 := make(chan struct{})

	if err := scope.Go(func(ctx context.Context) {
		close(started1)
		<-release
	}); err != nil {
		t.Fatalf("unexpected error on goroutine 1: %v", err)
	}

	if err := scope.Go(func(ctx context.Context) {
		close(started2)
		<-release
	}); err != nil {
		t.Fatalf("unexpected error on goroutine 2: %v", err)
	}

	<-started1
	<-started2

	// Third goroutine should exceed budget
	err := scope.Go(func(ctx context.Context) {})
	if err == nil {
		t.Fatal("expected goroutine budget error, got nil")
	}

	close(release)
	_ = scope.Close(context.Background())
}

func TestManager_ShutdownDeadlineBoundsLegacyPlugin(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)
	p := &blockingLifecyclePlugin{
		lifecyclePlugin: lifecyclePlugin{name: "blocked", commands: []core.Command{{Name: "blocked"}}},
		started:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := mgr.ShutdownWithContext(ctx)
	elapsed := time.Since(start)
	close(p.release)

	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected shutdown deadline error, got %v", err)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("legacy plugin shutdown escaped lifecycle deadline: %v", elapsed)
	}
	select {
	case <-p.started:
	default:
		t.Fatal("legacy shutdown callback was never started")
	}
}

func TestManagerLifecycleUsesSharedCallbackBudget(t *testing.T) {
	exec := runtime.NewCallbackExecutor(1)
	release := make(chan struct{})
	ctx1, cancel1 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := exec.Run(ctx1, func() error { <-release; return nil }); !errors.Is(err, context.DeadlineExceeded) {
		cancel1()
		close(release)
		t.Fatalf("failed to occupy cleanup executor: %v", err)
	}
	cancel1()

	mgr := NewManager(core.NewRouter("."))
	mgr.SetCleanupExecutor(exec)
	p := &lifecyclePlugin{name: "bounded", commands: []core.Command{{Name: "bounded"}}}
	if err := mgr.Register(p); err != nil {
		close(release)
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := mgr.ShutdownWithContext(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("expected deadline waiting for shared lifecycle budget, got %v", err)
	}
	if p.shutdowns.Load() != 0 {
		close(release)
		t.Fatal("plugin shutdown started despite exhausted shared callback budget")
	}
	stats := mgr.CleanupStats()
	if stats.Capacity != 1 || stats.Active != 1 {
		close(release)
		t.Fatalf("unexpected cleanup stats: %+v", stats)
	}
	close(release)
}

func TestManagerRegistrationValidatorRollsBackStagedPlugin(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)
	rejected := errors.New("surface collision")
	calls := 0
	mgr.SetRegistrationValidator(func(context.Context) error {
		calls++
		if _, ok := router.Find("reserved"); ok {
			return rejected
		}
		return nil
	})

	p := &lifecyclePlugin{name: "collision", commands: []core.Command{{Name: "reserved"}}}
	err := mgr.Register(p)
	if !errors.Is(err, rejected) {
		t.Fatalf("Register() error=%v, want %v", err, rejected)
	}
	if calls != 1 {
		t.Fatalf("validator calls=%d, want 1", calls)
	}
	if _, ok := router.Find("reserved"); ok {
		t.Fatal("rejected staged command leaked into router")
	}
	if _, ok := mgr.Find("collision"); ok {
		t.Fatal("rejected plugin was committed to manager")
	}
	if p.shutdowns.Load() != 1 {
		t.Fatalf("rejected plugin cleanup count=%d, want 1", p.shutdowns.Load())
	}
}

func TestManagerRegistrationValidatorRollsBackEnable(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)
	p := &lifecyclePlugin{name: "reload_collision", commands: []core.Command{{Name: "reserved_reload"}}}
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Disable(context.Background(), p.Name()); err != nil {
		t.Fatal(err)
	}
	if mgr.IsEnabled(p.Name()) {
		t.Fatal("plugin remained enabled after Disable")
	}

	rejected := errors.New("surface collision")
	mgr.SetRegistrationValidator(func(context.Context) error {
		if _, ok := router.Find("reserved_reload"); ok {
			return rejected
		}
		return nil
	})
	err := mgr.Enable(context.Background(), p.Name())
	if !errors.Is(err, rejected) {
		t.Fatalf("Enable() error=%v, want %v", err, rejected)
	}
	if mgr.IsEnabled(p.Name()) {
		t.Fatal("rejected generation was marked enabled")
	}
	if _, ok := router.Find("reserved_reload"); ok {
		t.Fatal("rejected re-enabled command leaked into router")
	}
	if p.shutdowns.Load() != 2 {
		t.Fatalf("shutdown count=%d, want 2 (disable + rejected enable rollback)", p.shutdowns.Load())
	}

	mgr.SetRegistrationValidator(nil)
	if err := mgr.Enable(context.Background(), p.Name()); err != nil {
		t.Fatalf("Enable(after removing validator) error=%v", err)
	}
	if !mgr.IsEnabled(p.Name()) {
		t.Fatal("plugin did not recover after collision cleared")
	}
	if _, ok := router.Find("reserved_reload"); !ok {
		t.Fatal("successful re-enable did not restore command")
	}
}
