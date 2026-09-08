package system

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:          "system",
		Version:     "1.0.0",
		Description: "System metrics, diagnostic information, and restart controls",
	}
}

func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	if rt.Plugins == nil {
		return module.ErrNilPluginManager
	}
	plugin := New()
	if rt.Metrics != nil {
		plugin.SetMetrics(rt.Metrics)
	}
	return rt.Plugins.RegisterWithContext(ctx, plugin)
}

var _ module.Module = ModuleType{}
