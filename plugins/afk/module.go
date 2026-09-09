package afk

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
		ID:           "afk",
		Version:      "1.0.0",
		Description:  "Away From Keyboard status manager and intelligent auto-reply system",
		Capabilities: []string{plugin.CapTelegramSendMessage, plugin.CapTelegramDeleteMessage, plugin.CapStorageRead, plugin.CapStorageWrite, plugin.CapEvents},
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
	p := New(repo, rt.OwnerID, rt.TelegramService)
	if rt.Logger != nil {
		p.SetLogger(rt.Logger)
	}
	if rt.Resolver != nil {
		p.SetResolver(rt.Resolver)
	}
	return rt.RegisterPlugin(ctx, m.Manifest(), p)
}

func (ModuleType) Migrations() []database.Migration {
	return Migrations()
}

var _ module.Module = ModuleType{}
var _ database.MigrationProvider = ModuleType{}
