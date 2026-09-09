package voice

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
	voiceSvc "github.com/inipew/goultroid/internal/voice"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "voice",
		Version:      "1.0.0",
		Description:  "Voice chat music and media stream player",
		Capabilities: []string{plugin.CapFilesystemTemp, plugin.CapProcessExecute, plugin.CapTelegramSendMessage},
	}
}

func (ModuleType) Migrations() []database.Migration {
	return Migrations()
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	var repo voiceSvc.Repository
	if rt.DB != nil {
		repo = NewSQLiteRepository(rt.DB)
	}
	_ = repo
	p := New(nil)
	return rt.RegisterPlugin(ctx, m.Manifest(), p)
}

var (
	_ module.Module              = ModuleType{}
	_ database.MigrationProvider = ModuleType{}
)
