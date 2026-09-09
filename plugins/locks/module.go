package locks

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "locks",
		Version:      "1.0.0",
		Description:  "Chat permissions locking and unlocking",
		Capabilities: []string{plugin.CapTelegramSendMessage, plugin.CapTelegramDeleteMessage, plugin.CapEvents},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	return rt.RegisterPlugin(ctx, m.Manifest(), New())
}

var _ module.Module = ModuleType{}
