package plugin

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
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
