package pmpermit

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "pmpermit",
		Version:      "1.0.0",
		Description:  "Anti-spam shield and private message access control system",
		Capabilities: []string{plugin.CapTelegramSendMessage, plugin.CapStorageRead, plugin.CapStorageWrite, plugin.CapEvents},
	}
}

func (ModuleType) Migrations() []database.Migration {
	return Migrations()
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	p := New(rt.PMPermitService)
	return rt.RegisterPlugin(ctx, m.Manifest(), p)
}

var (
	_ module.Module              = ModuleType{}
	_ database.MigrationProvider = ModuleType{}
)
