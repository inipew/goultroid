package notes

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/savedresponse"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:          "notes",
		Version:     "1.5.1",
		Description: "Chat notes management and retrieval",
		Capabilities: []string{
			plugin.CapTelegramSendMessage,
			plugin.CapStorageRead,
			plugin.CapStorageWrite,
			plugin.CapFilesystemTemp,
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
	repo := NewSQLiteRepository(rt.DB)
	return rt.RegisterPlugin(ctx, m.Manifest(), New(repo, savedresponse.NewService(rt.Storage, rt.DB)))
}

func (ModuleType) Migrations() []database.Migration {
	return Migrations()
}

var _ module.Module = ModuleType{}
var _ database.MigrationProvider = ModuleType{}
