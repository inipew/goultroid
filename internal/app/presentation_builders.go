package app

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/presentation"
)

const corePresentationGeneration uint64 = 1

func resolvePresentationGeneration(manager *plugin.Manager, owner string) (uint64, bool) {
	if owner == "core" {
		return corePresentationGeneration, true
	}
	if manager == nil {
		return 0, false
	}
	scope, ok := manager.Scope(owner)
	if !ok {
		return 0, false
	}
	return scope.Generation(), true
}

type defaultScreenBuilder struct {
	key presentation.ScreenKey
	fn  func(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error)
}

func (b *defaultScreenBuilder) Key() presentation.ScreenKey { return b.key }
func (b *defaultScreenBuilder) Build(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
	return b.fn(ctx, req)
}

// registerDefaultScreens registers the canonical built-in core screens in presentation.Registry.
func registerDefaultScreens(reg *presentation.Registry, router *core.Router, startTime time.Time) error {
	getUsername := func() string {
		return "GoUltroidBot"
	}
	getUptime := func() time.Duration {
		if !startTime.IsZero() {
			return time.Since(startTime)
		}
		return 0
	}
	getCommands := func() []core.Command {
		if router != nil {
			return router.All()
		}
		return nil
	}

	// 1. Core Start Screen (core:start:v1)
	_, err := reg.Register(presentation.Registration{
		Owner:      "core",
		Generation: corePresentationGeneration,
		Builder: &defaultScreenBuilder{
			key: presentation.ScreenKey{Namespace: "core", Name: "start", Version: 1},
			fn: func(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
				screen := menu.BuildStartScreenWithCommands(getUsername(), getUptime(), getCommands())
				return presentation.BuildResult{Screen: screen}, nil
			},
		},
		Policy: presentation.PublicPolicy(),
	})
	if err != nil {
		return err
	}

	// 2. Core Help Screen (core:help:v1)
	_, err = reg.Register(presentation.Registration{
		Owner:      "core",
		Generation: corePresentationGeneration,
		Builder: &defaultScreenBuilder{
			key: presentation.ScreenKey{Namespace: "core", Name: "help", Version: 1},
			fn: func(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
				screen := menu.BuildHelpScreenWithCommands(getUsername(), getCommands())
				return presentation.BuildResult{Screen: screen}, nil
			},
		},
		Policy: presentation.PublicPolicy(),
	})
	if err != nil {
		return err
	}

	// 3. Core Settings Screen (core:settings:v1)
	_, err = reg.Register(presentation.Registration{
		Owner:      "core",
		Generation: corePresentationGeneration,
		Builder: &defaultScreenBuilder{
			key: presentation.ScreenKey{Namespace: "core", Name: "settings", Version: 1},
			fn: func(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
				screen := menu.BuildSettingsScreen(getUsername())
				return presentation.BuildResult{Screen: screen, Sensitivity: presentation.SensitivitySensitive}, nil
			},
		},
		Policy: presentation.OwnerOnlyPolicy(),
	})
	if err != nil {
		return err
	}

	// 4. Core Status Screen (core:status:v1)
	_, err = reg.Register(presentation.Registration{
		Owner:      "core",
		Generation: corePresentationGeneration,
		Builder: &defaultScreenBuilder{
			key: presentation.ScreenKey{Namespace: "core", Name: "status", Version: 1},
			fn: func(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
				screen := menu.BuildStatusScreen(getUsername(), getUptime(), "GoUltroid Core")
				return presentation.BuildResult{Screen: screen}, nil
			},
		},
		Policy: presentation.PublicPolicy(),
	})
	return err
}
