package userlog

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:          "userlog",
		Version:     "1.0.0",
		Description: "User event logging to a dedicated Telegram destination",
	}
}

func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	if rt.Plugins == nil {
		return module.ErrNilPluginManager
	}
	p := New(rt.UserlogService, rt.OwnerID)
	if rt.EventBus != nil {
		p.SetEventBus(rt.EventBus)
	}
	return rt.Plugins.RegisterWithContext(ctx, p)
}

var _ module.Module = ModuleType{}
