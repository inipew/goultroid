package wikipedia

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "wikipedia",
		Version:      "1.0.0",
		Description:  "Search and summarize Wikipedia articles",
		Capabilities: []string{plugin.CapHTTP},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	return rt.RegisterPlugin(ctx, m.Manifest(), New())
}

var _ module.Module = ModuleType{}
