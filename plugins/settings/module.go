package settings

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "settings",
		Version:      "1.0.0",
		Description:  "Interactive settings dashboard and configuration management",
		Capabilities: []string{plugin.CapStorageRead, plugin.CapStorageWrite, plugin.CapTelegramSendMessage},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	p := New(rt.SettingsService, rt.CallbackStore)
	if rt.Logger != nil {
		p.SetLogger(rt.Logger)
	}
	if rt.Callbacks != nil {
		if err := rt.Callbacks.Register(p); err != nil {
			return fmt.Errorf("register settings callback handler: %w", err)
		}
	}
	return rt.RegisterPlugin(ctx, m.Manifest(), p)
}

var _ module.Module = ModuleType{}
