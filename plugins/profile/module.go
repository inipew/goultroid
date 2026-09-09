package profile

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "profile",
		Version:      "1.0.0",
		Description:  "Self-user profile and contact management commands",
		Capabilities: []string{plugin.CapFilesystemTemp},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	return rt.RegisterPlugin(ctx, m.Manifest(), New())
}

var _ module.Module = ModuleType{}
