package system

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "system",
		Version:      "1.0.0",
		Description:  "System metrics, diagnostic information, and restart controls",
		Capabilities: []string{plugin.CapProcessExecute, plugin.CapFilesystemTemp},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	p := New()
	if rt.Metrics != nil {
		p.SetMetrics(rt.Metrics)
	}
	return rt.RegisterPlugin(ctx, m.Manifest(), p)
}

var _ module.Module = ModuleType{}
