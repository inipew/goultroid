package userlog

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "userlog",
		Version:      "1.0.0",
		Description:  "User event logging to a dedicated Telegram destination",
		Capabilities: []string{plugin.CapTelegramSendMessage, plugin.CapStorageRead, plugin.CapStorageWrite, plugin.CapEvents},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	p := New(rt.UserlogService, rt.OwnerID)
	if rt.EventBus != nil {
		p.SetEventBus(rt.EventBus)
	}
	return rt.RegisterPlugin(ctx, m.Manifest(), p)
}

var _ module.Module = ModuleType{}
