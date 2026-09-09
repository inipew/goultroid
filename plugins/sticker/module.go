package sticker

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "sticker",
		Version:      "1.0.0",
		Description:  "Sticker creation and conversion utilities",
		Capabilities: []string{plugin.CapFilesystemTemp},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	return rt.RegisterPlugin(ctx, m.Manifest(), New())
}

var _ module.Module = ModuleType{}
