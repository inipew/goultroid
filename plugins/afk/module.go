package afk

import (
	"context"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type runtimeTelegramService struct {
	core.MessageServicer
	module.BotOriginTracker
}

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "afk",
		Version:      "1.2.0",
		Description:  "Away From Keyboard status manager and intelligent auto-reply system",
		Capabilities: []string{plugin.CapTelegramRead, plugin.CapTelegramSendMessage, plugin.CapTelegramDeleteMessage, plugin.CapStorageRead, plugin.CapStorageWrite, plugin.CapEvents, plugin.CapTasks},
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
	p := NewWithService(repo, rt.OwnerID, func() TelegramService {
		if rt.MessageService == nil || rt.OriginTracker == nil {
			return nil
		}
		messages := rt.MessageService()
		origin := rt.OriginTracker()
		if messages == nil || origin == nil {
			return nil
		}
		return &runtimeTelegramService{MessageServicer: messages, BotOriginTracker: origin}
	})
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
