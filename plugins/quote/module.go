package quote

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:          "quote",
		Version:     "1.2.0",
		Description: "Render a replied message as a shareable quote image",
	}
}

func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	if rt.Plugins == nil {
		return module.ErrNilPluginManager
	}
	return rt.Plugins.RegisterWithContext(ctx, New())
}

var _ module.Module = ModuleType{}
