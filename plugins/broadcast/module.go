package broadcast

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/savedresponse"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "broadcast",
		Version:      "1.2.1",
		Description:  "Mass messaging tool with FloodWait resilience and target filtering",
		Capabilities: []string{plugin.CapTelegramSendMessage, plugin.CapFilesystemTemp, plugin.CapTasks},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	responses := savedresponse.NewService(rt.Storage, rt.DB)
	p := New(rt.BroadcastService, responses)
	return rt.RegisterPlugin(ctx, m.Manifest(), p)
}

var _ module.Module = ModuleType{}
