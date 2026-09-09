package filters

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
		ID:           "filters",
		Version:      "1.0.0",
		Description:  "Chat-specific auto-reply keyword filters",
		Capabilities: []string{plugin.CapTelegramSendMessage, plugin.CapStorageRead, plugin.CapStorageWrite, plugin.CapEvents},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	if rt.DB == nil {
		return module.ErrNilDatabase
	}
	repo := NewSQLiteRepository(rt.DB)
	return rt.RegisterPlugin(ctx, m.Manifest(), New(repo, rt.TelegramService))
}

func (ModuleType) Migrations() []database.Migration {
	return Migrations()
}

var _ module.Module = ModuleType{}
var _ database.MigrationProvider = ModuleType{}
