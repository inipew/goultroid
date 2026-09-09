package clone

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

// ModuleType is the compile-time composition boundary for the clone feature.
type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "clone",
		Version:      "1.0.0",
		Description:  "Clone another user's public profile identity and safely revert it",
		Capabilities: []string{plugin.CapTelegramSendMessage, plugin.CapStorageRead, plugin.CapStorageWrite},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	if rt.DB == nil {
		return module.ErrNilDatabase
	}
	return rt.RegisterPlugin(ctx, m.Manifest(), New(NewSQLiteRepository(rt.DB), rt.OwnerID))
}

func (ModuleType) Migrations() []database.Migration {
	return Migrations()
}

var _ module.Module = ModuleType{}
var _ database.MigrationProvider = ModuleType{}
