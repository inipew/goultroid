package quote

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "quote",
		Version:      "1.2.0",
		Description:  "Render a replied message as a shareable quote image",
		Capabilities: []string{plugin.CapTelegramSendMessage},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	return rt.RegisterPlugin(ctx, m.Manifest(), New())
}

var _ module.Module = ModuleType{}
