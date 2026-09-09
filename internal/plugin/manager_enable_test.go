package plugin

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/core"
)

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
