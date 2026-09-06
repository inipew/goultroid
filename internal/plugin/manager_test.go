package plugin

import (
	"errors"
	"testing"

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
	return d.commands
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
