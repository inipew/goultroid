package clone

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
)

// ModuleType is the compile-time composition boundary for the clone feature.
type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:          "clone",
		Version:     "1.0.0",
		Description: "Clone another user's public profile identity and safely revert it",
	}
}

func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	if rt.DB == nil {
		return module.ErrNilDatabase
	}
	if rt.Plugins == nil {
		return module.ErrNilPluginManager
	}
	return rt.Plugins.RegisterWithContext(ctx, New(rt.DB, rt.OwnerID))
}
