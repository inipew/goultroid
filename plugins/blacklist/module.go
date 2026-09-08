package blacklist

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:          "blacklist",
		Version:     "1.0.0",
		Description: "Chat-specific keyword blacklist and auto-deletion",
	}
}

func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	if rt.DB == nil {
		return module.ErrNilDatabase
	}
	if rt.Plugins == nil {
		return module.ErrNilPluginManager
	}
	repo := NewSQLiteRepository(rt.DB)
	return rt.Plugins.RegisterWithContext(ctx, New(repo, rt.TelegramService))
}

func (ModuleType) Migrations() []database.Migration {
	return Migrations()
}

var _ module.Module = ModuleType{}
var _ database.MigrationProvider = ModuleType{}
