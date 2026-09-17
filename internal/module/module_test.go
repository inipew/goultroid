package module

import (
	"context"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/ui"
)

type testScreenBuilder struct {
	key presentation.ScreenKey
}

func (b *testScreenBuilder) Key() presentation.ScreenKey {
	return b.key
}

func (b *testScreenBuilder) Build(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
	return presentation.BuildResult{Screen: ui.NewScreen("test", "Title", "Body")}, nil
}

type dummyPlugin struct {
	name string
}

func (d *dummyPlugin) Name() string             { return d.name }
func (d *dummyPlugin) Commands() []core.Command { return nil }
func (d *dummyPlugin) Init() error              { return nil }
func (d *dummyPlugin) Shutdown() error          { return nil }

func TestRuntime_RegisterScreen_FailClosedWhenScopeNotFound(t *testing.T) {
	reg := presentation.NewRegistry()
	eval := presentation.NewEvaluator(1, nil)
	presSvc := presentation.NewService(reg, eval)

	router := core.NewRouter(".")
	mgr := plugin.NewManager(router)

	rt := &Runtime{
		CoreRuntime: CoreRuntime{
			Plugins: mgr,
		},
		TelegramRuntime: TelegramRuntime{
			Presentation: presSvc,
		},
	}

	key := presentation.ScreenKey{Namespace: "custom", Name: "screen1", Version: 1}
	_, err := rt.RegisterScreen("unknown_plugin", presentation.Registration{
		Builder: &testScreenBuilder{key: key},
		Policy:  presentation.PublicPolicy(),
	})
	if err == nil {
		t.Fatal("expected error registering screen for missing plugin scope, got nil")
	}
	if !strings.Contains(err.Error(), "plugin scope \"unknown_plugin\" not found") {
		t.Fatalf("unexpected error message: %v", err)
	}

	if _, ok := reg.Resolve(key); ok {
		t.Fatal("screen was registered despite missing scope")
	}
}

func TestRuntime_RegisterScreen_NilPluginsWhenScoped(t *testing.T) {
	reg := presentation.NewRegistry()
	eval := presentation.NewEvaluator(1, nil)
	presSvc := presentation.NewService(reg, eval)

	rt := &Runtime{
		TelegramRuntime: TelegramRuntime{
			Presentation: presSvc,
		},
	}

	key := presentation.ScreenKey{Namespace: "custom", Name: "screen1", Version: 1}
	_, err := rt.RegisterScreen("some_plugin", presentation.Registration{
		Builder: &testScreenBuilder{key: key},
		Policy:  presentation.PublicPolicy(),
	})
	if err == nil {
		t.Fatal("expected error when rt.Plugins is nil for scoped registration, got nil")
	}
}

func TestRuntime_RegisterScreen_ScopedSuccessAndRevocation(t *testing.T) {
	ctx := context.Background()
	reg := presentation.NewRegistry()
	eval := presentation.NewEvaluator(1, nil)
	presSvc := presentation.NewService(reg, eval)

	router := core.NewRouter(".")
	mgr := plugin.NewManager(router)
	mgr.SetPresentationRevoker(reg)

	rt := &Runtime{
		CoreRuntime: CoreRuntime{
			Plugins: mgr,
		},
		TelegramRuntime: TelegramRuntime{
			Presentation: presSvc,
		},
	}

	p := &dummyPlugin{name: "myplugin"}
	err := rt.RegisterPlugin(ctx, Manifest{ID: "myplugin", Version: "1.0.0"}, p)
	if err != nil {
		t.Fatalf("RegisterPlugin failed: %v", err)
	}

	key := presentation.ScreenKey{Namespace: "myplugin", Name: "dashboard", Version: 1}
	lease, err := rt.RegisterScreen("myplugin", presentation.Registration{
		Builder: &testScreenBuilder{key: key},
		Policy:  presentation.PublicPolicy(),
	})
	if err != nil {
		t.Fatalf("RegisterScreen failed: %v", err)
	}
	if lease == nil {
		t.Fatal("expected non-nil lease")
	}

	// Verify screen is registered with proper Owner and Generation
	found, ok := reg.Resolve(key)
	if !ok {
		t.Fatal("expected screen to be registered in registry")
	}
	if found.Owner != "plugin:myplugin" {
		t.Errorf("expected owner 'plugin:myplugin', got %q", found.Owner)
	}
	if found.Generation == 0 {
		t.Errorf("expected non-zero generation, got %d", found.Generation)
	}

	// Disable plugin -> screen should be revoked
	err = mgr.Disable(ctx, "myplugin")
	if err != nil {
		t.Fatalf("Disable plugin failed: %v", err)
	}

	if _, ok := reg.Resolve(key); ok {
		t.Fatal("expected screen to be revoked when plugin was disabled")
	}
}

func TestRuntime_RegisterScreen_Unscoped(t *testing.T) {
	reg := presentation.NewRegistry()
	eval := presentation.NewEvaluator(1, nil)
	presSvc := presentation.NewService(reg, eval)

	rt := &Runtime{
		TelegramRuntime: TelegramRuntime{
			Presentation: presSvc,
		},
	}

	key := presentation.ScreenKey{Namespace: "core", Name: "help", Version: 1}
	lease, err := rt.RegisterScreen("", presentation.Registration{
		Builder: &testScreenBuilder{key: key},
		Policy:  presentation.PublicPolicy(),
	})
	if err != nil {
		t.Fatalf("RegisterScreen unscoped failed: %v", err)
	}
	defer lease.Close()

	found, ok := reg.Resolve(key)
	if !ok {
		t.Fatal("expected unscoped screen to be registered")
	}
	if found.Owner != "" || found.Generation != 0 {
		t.Errorf("expected empty owner and 0 generation for unscoped screen, got %q / %d", found.Owner, found.Generation)
	}
}
