package downloader

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "downloader",
		Version:      "1.0.0",
		Description:  "Media download capabilities for Telegram media and external URLs",
		Capabilities: []string{plugin.CapHTTP, plugin.CapFilesystemTemp, plugin.CapFilesystemData, plugin.CapProcessExecute, plugin.CapTelegramSendMessage, plugin.CapJobs},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	p := New(rt.DownloadRegistry, rt.Storage)
	return rt.RegisterPlugin(ctx, m.Manifest(), p)
}

var _ module.Module = ModuleType{}
