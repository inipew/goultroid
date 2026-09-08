package app

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
)

//go:generate go run ../../tools/featuregen

func registerBuiltinModules(ctx context.Context, rt *module.Runtime) error {
	ordered, err := module.ResolveOrder(builtinModules)
	if err != nil {
		return fmt.Errorf("resolve module dependencies: %w", err)
	}
	for _, m := range ordered {
		if err := m.Register(ctx, rt); err != nil {
			return fmt.Errorf("failed to register feature module %q: %w", m.Manifest().ID, err)
		}
	}
	return nil
}

func migrateBuiltinFeatures(ctx context.Context, db *database.DB) error {
	providers := make([]database.MigrationProvider, 0, len(builtinModules))
	for _, m := range builtinModules {
		provider, ok := m.(database.MigrationProvider)
		if !ok {
			continue
		}
		providers = append(providers, provider)
	}
	if err := database.RunFeatureMigrations(ctx, db, providers...); err != nil {
		return fmt.Errorf("feature migrations failed: %w", err)
	}
	return nil
}
