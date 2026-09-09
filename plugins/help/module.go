package help

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
		ID:           "help",
		Version:      "1.0.0",
		Description:  "Interactive help and command documentation browser",
		Capabilities: []string{plugin.CapTelegramSendMessage},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
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
	return rt.RegisterPlugin(ctx, m.Manifest(), p)
}

var _ module.Module = ModuleType{}
