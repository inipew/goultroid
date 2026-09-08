package app

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/module"
)

//go:generate go run ../../tools/featuregen

func registerBuiltinModules(ctx context.Context, rt *module.Runtime) error {
	for _, m := range builtinModules {
		if err := module.Validate(m); err != nil {
			return fmt.Errorf("invalid feature module: %w", err)
		}
		if err := m.Register(ctx, rt); err != nil {
			return fmt.Errorf("failed to register feature module %q: %w", m.Manifest().ID, err)
		}
	}
	return nil
}
