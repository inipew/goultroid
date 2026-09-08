package help

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/module"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:          "help",
		Version:     "1.0.0",
		Description: "Interactive help and command documentation browser",
	}
}

func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	if rt.Plugins == nil {
		return module.ErrNilPluginManager
	}
	p := New(rt.Router)
	if rt.CallbackStore != nil {
		p.SetStateStore(rt.CallbackStore)
	}
	if rt.Callbacks != nil {
		if err := rt.Callbacks.Register(p); err != nil {
			return fmt.Errorf("register help callback handler: %w", err)
		}
	}
	return rt.Plugins.RegisterWithContext(ctx, p)
}

var _ module.Module = ModuleType{}
