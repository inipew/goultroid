package myxl

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
		ID:          "myxl",
		Version:     "1.1.0",
		Description: "MyXL account management and real-time quota visualizer",
		Capabilities: []string{
			plugin.CapTelegramSendMessage,
			plugin.CapStorageRead,
			plugin.CapStorageWrite,
			plugin.CapHTTP,
			plugin.CapSecretRead,
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
	client := NewClient(DefaultClientConfig(), repo, nil)
	p := New(repo, client)

	if rt.CallbackStore != nil {
		p.SetStateStore(rt.CallbackStore)
	}
	return rt.RegisterPlugin(ctx, m.Manifest(), p)
}

func (ModuleType) Migrations() []database.Migration {
	return Migrations()
}

var _ module.Module = ModuleType{}
var _ database.MigrationProvider = ModuleType{}
