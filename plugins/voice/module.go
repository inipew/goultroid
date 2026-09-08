package voice

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
	voiceSvc "github.com/inipew/goultroid/internal/voice"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:          "voice",
		Version:     "1.0.0",
		Description: "Voice chat music and media stream player",
	}
}

func (ModuleType) Migrations() []database.Migration {
	return Migrations()
}

func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	if rt == nil {
		return module.ErrNilRuntime
	}
	if rt.Plugins == nil {
		return module.ErrNilPluginManager
	}
	var repo voiceSvc.Repository
	if rt.DB != nil {
		repo = NewSQLiteRepository(rt.DB)
	}
	_ = repo
	p := New(nil)
	return rt.Plugins.RegisterWithContext(ctx, p)
}

var (
	_ module.Module              = ModuleType{}
	_ database.MigrationProvider = ModuleType{}
)
