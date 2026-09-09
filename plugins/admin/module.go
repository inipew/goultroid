package admin

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
	moderationSvc "github.com/inipew/goultroid/internal/services/moderation"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "admin",
		Version:      "1.0.0",
		Description:  "Group administration and moderation commands",
		Capabilities: []string{plugin.CapTelegramSendMessage, plugin.CapTelegramDeleteMessage, plugin.CapEvents},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	if rt.DB == nil {
		return module.ErrNilDatabase
	}

	repo := NewSQLiteWarningRepository(rt.DB)
	moderationService := moderationSvc.NewService(repo, rt.TelegramService, rt.Logger)
	return rt.RegisterPlugin(ctx, m.Manifest(), New(moderationService))
}

func (ModuleType) Migrations() []database.Migration {
	return Migrations()
}

var _ module.Module = ModuleType{}
var _ database.MigrationProvider = ModuleType{}
