package downloader

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:          "downloader",
		Version:     "1.0.0",
		Description: "Media download capabilities for Telegram media and external URLs",
		Capabilities: []string{
			plugin.CapHTTP,
			plugin.CapFilesystemTemp,
			plugin.CapFilesystemData,
			plugin.CapProcessExecute,
			plugin.CapTelegramRead,
			plugin.CapTelegramSendMessage,
			plugin.CapTasks,
		},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	if rt.DB == nil {
		return module.ErrNilDatabase
	}
	p := New(rt.DownloadRegistry, rt.Storage, mediaregistry.New(rt.DB))
	return rt.RegisterPlugin(ctx, m.Manifest(), p)
}

var _ module.Module = ModuleType{}
