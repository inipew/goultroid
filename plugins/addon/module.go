package addon

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "addon",
		Name:         "Addon",
		Version:      "1.0.0",
		Description:  "Manage external userbot addons, manifests, and capability permissions",
		Capabilities: []string{plugin.CapProcessExecute, plugin.CapTelegramSendMessage},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	p := New(rt.AddonManager)
	return rt.RegisterPlugin(ctx, m.Manifest(), p)
}

var _ module.Module = ModuleType{}
