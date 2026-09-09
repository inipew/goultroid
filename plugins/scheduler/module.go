package scheduler

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "scheduler",
		Version:      "1.0.0",
		Description:  "Message, reminder, and recurring command scheduler",
		Capabilities: []string{plugin.CapScheduler, plugin.CapJobs, plugin.CapTelegramSendMessage},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	p := New(rt.SchedEngine)
	return rt.RegisterPlugin(ctx, m.Manifest(), p)
}

var _ module.Module = ModuleType{}
