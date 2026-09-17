package myxl

import (
	"context"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/presentation"
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
	if rt.AssistantMenu != nil {
		p.SetAssistantMenu(rt.AssistantMenu)
	}
	if rt.Presentation != nil {
		p.SetPresentation(rt.Presentation)
	}
	if rt.Handoffs != nil {
		p.SetHandoffs(rt.Handoffs)
	}

	if err := rt.RegisterPlugin(ctx, m.Manifest(), p); err != nil {
		return err
	}

	if rt.Presentation != nil {
		_, err := rt.RegisterScreen("myxl", presentation.Registration{
			Builder: &dashboardScreenBuilder{p: p},
			Policy: presentation.AccessPolicy{
				Permission:     presentation.PermissionOwner,
				AllowedSources: execution.SurfaceAll,
				RequirePrivate: true,
				Sensitive:      true,
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

type dashboardScreenBuilder struct {
	p *Plugin
}

func (b *dashboardScreenBuilder) Key() presentation.ScreenKey {
	return presentation.ScreenKey{Namespace: "myxl", Name: "dashboard", Version: 1}
}

func (b *dashboardScreenBuilder) Build(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
	mask := req.ChatType != presentation.ChatTypePrivate
	screen, err := b.p.menuMgr.BuildDashboardScreen(ctx, mask)
	if err != nil {
		return presentation.BuildResult{}, err
	}
	return presentation.BuildResult{
		Screen:      screen,
		Sensitivity: presentation.SensitivitySensitive,
	}, nil
}

func (ModuleType) Migrations() []database.Migration {
	return Migrations()
}

var _ module.Module = ModuleType{}
var _ database.MigrationProvider = ModuleType{}
