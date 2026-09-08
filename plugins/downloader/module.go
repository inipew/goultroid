package downloader

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:          "downloader",
		Version:     "1.0.0",
		Description: "Media download capabilities for Telegram media and external URLs",
	}
}

func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	if rt.Plugins == nil {
		return module.ErrNilPluginManager
	}
	p := New(rt.DownloadRegistry, rt.Storage)
	return rt.Plugins.RegisterWithContext(ctx, p)
}

var _ module.Module = ModuleType{}
