package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type dummyPlugin struct {
	name     string
	initErr  error
	shutErr  error
	shutDone bool
	commands []core.Command
}

func (d *dummyPlugin) Name() string {
	return d.name
}

func (d *dummyPlugin) Commands() []core.Command {
	cmds := make([]core.Command, len(d.commands))
	copy(cmds, d.commands)
	for i := range cmds {
		if cmds[i].Handler == nil {
			cmds[i].Handler = func(ctx *core.Context) error { return nil }
		}
	}
	return cmds
}

func (d *dummyPlugin) Init() error {
	return d.initErr
}

func (d *dummyPlugin) Shutdown() error {
	d.shutDone = true
	return d.shutErr
}

func TestManager_RegisterAndFind(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)

	p := &dummyPlugin{
		name: "ping",
		commands: []core.Command{
			{Name: "ping", Aliases: []string{"p"}},
		},
	}

	if err := mgr.Register(p); err != nil {
		t.Fatalf("unexpected error registering plugin: %v", err)
	}

	// Verify plugin list
	plugins := mgr.Plugins()
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(plugins))
	}
	if plugins[0].Name() != "ping" {
		t.Errorf("expected plugin name 'ping', got %q", plugins[0].Name())
	}

	// Find plugin
	found, ok := mgr.Find("PING")
	if !ok || found.Name() != "ping" {
		t.Errorf("expected to find plugin 'ping', got %v, %v", ok, found)
	}

	// Command was registered into router
	cmd, exists := router.Find("p")
	if !exists || cmd.Name != "ping" {
		t.Errorf("expected command alias 'p' to be registered in router")
	}

	// Duplicate registration
	if err := mgr.Register(p); err == nil {
		t.Errorf("expected error on duplicate plugin registration")
	}
}

func TestManager_ValidationErrors(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)

	// Nil plugin
	if err := mgr.Register(nil); err == nil {
		t.Errorf("expected error registering nil plugin")
	}

	// Empty name
	pEmpty := &dummyPlugin{name: ""}
	if err := mgr.Register(pEmpty); err == nil {
		t.Errorf("expected error registering plugin with empty name")
	}

	// Init error
	pInitErr := &dummyPlugin{name: "broken", initErr: errors.New("init boom")}
	if err := mgr.Register(pInitErr); err == nil {
		t.Errorf("expected error when plugin Init() fails")
	}

	// Command conflict
	p1 := &dummyPlugin{
		name: "plug1",
		commands: []core.Command{
			{Name: "common"},
		},
	}
	if err := mgr.Register(p1); err != nil {
		t.Fatalf("unexpected error registering plug1: %v", err)
	}

	p2 := &dummyPlugin{
		name: "plug2",
		commands: []core.Command{
			{Name: "common"},
		},
	}
	if err := mgr.Register(p2); err == nil {
		t.Errorf("expected error when command conflicts across plugins")
	}

	// Empty command name
	pEmptyCmd := &rawCommandPlugin{
		name: "empty_cmd_plugin",
		commands: []core.Command{
			{Name: "", Handler: func(ctx *core.Context) error { return nil }},
		},
	}
	if err := mgr.Register(pEmptyCmd); err == nil {
		t.Errorf("expected error when command name is empty")
	}

	// Nil command handler
	pNilHandler := &rawCommandPlugin{
		name: "nil_handler_plugin",
		commands: []core.Command{
			{Name: "valid_name", Handler: nil},
		},
	}
	if err := mgr.Register(pNilHandler); err == nil {
		t.Errorf("expected error when command handler is nil")
	}
}

type rawCommandPlugin struct {
	name     string
	commands []core.Command
}

func (r *rawCommandPlugin) Name() string                { return r.name }
func (r *rawCommandPlugin) Commands() []core.Command    { return r.commands }
func (r *rawCommandPlugin) Init() error                 { return nil }
func (r *rawCommandPlugin) Shutdown() error             { return nil }

type mockHookPlugin struct {
	dummyPlugin
	hookCalled bool
}

func (m *mockHookPlugin) HandleIncomingMessage(ctx context.Context, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) error {
	m.hookCalled = true
	return nil
}

func (m *mockHookPlugin) MessageHookPriority() int {
	return 42
}

type mockHookRegistrar struct {
	registered   int
	unregistered int
}

func (r *mockHookRegistrar) AddPrioritizedMessageHandler(priority int, handler MessageHookHandler) func() {
	r.registered++
	return func() {
		r.unregistered++
	}
}

func TestManager_HookRegistrationAndShutdown(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)
	registrar := &mockHookRegistrar{}
	mgr.SetHookRegistrar(registrar)

	hookPlug := &mockHookPlugin{
		dummyPlugin: dummyPlugin{name: "hook_plugin"},
	}

	if err := mgr.Register(hookPlug); err != nil {
		t.Fatalf("failed to register hook plugin: %v", err)
	}

	if registrar.registered != 1 {
		t.Errorf("expected 1 hook registered, got %d", registrar.registered)
	}

	if err := mgr.Shutdown(); err != nil {
		t.Fatalf("unexpected error shutting down: %v", err)
	}

	if registrar.unregistered != 1 {
		t.Errorf("expected 1 hook unregistered on shutdown, got %d", registrar.unregistered)
	}
}

func TestManager_Shutdown(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)

	p1 := &dummyPlugin{name: "p1"}
	p2 := &dummyPlugin{name: "p2", shutErr: errors.New("cannot close")}

	_ = mgr.Register(p1)
	_ = mgr.Register(p2)

	err := mgr.Shutdown()
	if err == nil {
		t.Errorf("expected error during shutdown, got nil")
	}

	if !p1.shutDone || !p2.shutDone {
		t.Errorf("expected both plugins to have Shutdown invoked")
	}
}

type describedPlugin struct {
	dummyPlugin
	meta Metadata
}

func (d *describedPlugin) Metadata() Metadata {
	return d.meta
}

func TestManager_Metadata(t *testing.T) {
	router := core.NewRouter(".")
	mgr := NewManager(router)

	// 1. Regular plugin (defaults populated)
	p1 := &dummyPlugin{name: "basic"}
	if err := mgr.Register(p1); err != nil {
		t.Fatalf("failed to register basic: %v", err)
	}

	m1, ok := mgr.GetMetadata("basic")
	if !ok {
		t.Fatalf("expected metadata for basic plugin")
	}
	if m1.Name != "basic" || m1.Version != "1.0.0" {
		t.Errorf("unexpected default metadata: %+v", m1)
	}

	// 2. Described plugin (custom metadata)
	p2 := &describedPlugin{
		dummyPlugin: dummyPlugin{name: "custom"},
		meta: Metadata{
			Name:        "custom",
			Version:     "2.4.0",
			Author:      "Pew",
			Description: "Custom extended plugin",
		},
	}
	if err := mgr.Register(p2); err != nil {
		t.Fatalf("failed to register custom: %v", err)
	}

	m2, ok := mgr.GetMetadata("CUSTOM")
	if !ok {
		t.Fatalf("expected metadata for custom plugin")
	}
	if m2.Version != "2.4.0" || m2.Author != "Pew" || m2.Description != "Custom extended plugin" {
		t.Errorf("unexpected custom metadata: %+v", m2)
	}

	// 3. AllMetadata
	all := mgr.AllMetadata()
	if len(all) != 2 {
		t.Fatalf("expected 2 metadata entries, got %d", len(all))
	}
	if all["custom"].Author != "Pew" {
		t.Errorf("unexpected all metadata entry for custom: %+v", all["custom"])
	}

	// 4. Nonexistent plugin
	_, ok = mgr.GetMetadata("nonexistent")
	if ok {
		t.Errorf("expected false for nonexistent plugin metadata")
	}
}
