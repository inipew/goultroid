package plugin_test

import (
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
)

type dummyPlugin struct {
	name string
}

func (d *dummyPlugin) Name() string             { return d.name }
func (d *dummyPlugin) Commands() []core.Command { return nil }
func (d *dummyPlugin) Init() error              { return nil }

func TestRegistry_Groups(t *testing.T) {
	if plugin.GetGroup("voice") != plugin.GroupVoice {
		t.Errorf("expected GroupVoice, got %s", plugin.GetGroup("voice"))
	}
	if plugin.GetGroup("admin") != plugin.GroupAdmin {
		t.Errorf("expected GroupAdmin, got %s", plugin.GetGroup("admin"))
	}
	if plugin.GetGroup("media") != plugin.GroupMedia {
		t.Errorf("expected GroupMedia, got %s", plugin.GetGroup("media"))
	}
	if plugin.GetGroup("unknown_plugin") != plugin.GroupCore {
		t.Errorf("expected default GroupCore, got %s", plugin.GetGroup("unknown_plugin"))
	}

	plugins := []plugin.Plugin{
		&dummyPlugin{name: "voice"},
		&dummyPlugin{name: "admin"},
		&dummyPlugin{name: "sudo"},
	}

	grouped := plugin.GroupedPlugins(plugins)
	if len(grouped[plugin.GroupVoice]) != 1 {
		t.Errorf("expected 1 voice plugin, got %d", len(grouped[plugin.GroupVoice]))
	}
	if len(grouped[plugin.GroupAdmin]) != 2 {
		t.Errorf("expected 2 admin plugins, got %d", len(grouped[plugin.GroupAdmin]))
	}
}
