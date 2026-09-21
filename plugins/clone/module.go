package clone

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
)

// ModuleType is the compile-time composition boundary for the clone feature.
type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "clone",
		Version:      "1.2.0",
		Description:  "Clone another user's public profile identity and safely revert it",
		Capabilities: []string{plugin.CapTelegramSendMessage, plugin.CapFilesystemTemp, plugin.CapTasks},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	if rt.DB == nil {
		return module.ErrNilDatabase
	}
	registry := mediaregistry.New(rt.DB)
	cloneStorage := mediaregistry.NewOwnerStorage(
		rt.Storage,
		registry,
		cloneRegistryProducer,
		cloneRegistryOwner,
		mediaregistry.LifecyclePersistent,
		"clone snapshot lifecycle cleanup",
	)
	return rt.RegisterPlugin(ctx, m.Manifest(), New(NewSQLiteRepository(rt.DB), rt.OwnerID, cloneStorage))
}

func (ModuleType) Migrations() []database.Migration {
	return Migrations()
}

var _ module.Module = ModuleType{}
var _ database.MigrationProvider = ModuleType{}
