package app

import (
	"context"

	"github.com/inipew/goultroid/internal/runtime"
)

// dependencyComponent adds composition-root dependencies without leaking app
// ownership concerns into reusable subsystem packages.
type dependencyComponent struct {
	runtime.Component
	dependencies []string
}

func (c dependencyComponent) Dependencies() []string {
	return append([]string(nil), c.dependencies...)
}

func (c dependencyComponent) Quiesce(ctx context.Context) error {
	if component, ok := c.Component.(runtime.Quiescer); ok {
		return component.Quiesce(ctx)
	}
	return nil
}

func (c dependencyComponent) Drain(ctx context.Context) error {
	if component, ok := c.Component.(runtime.Drainer); ok {
		return component.Drain(ctx)
	}
	return nil
}

type resourceComponent struct {
	name         string
	dependencies []string
	stop         func() error
}

func (c resourceComponent) Name() string { return c.name }
func (c resourceComponent) Dependencies() []string {
	return append([]string(nil), c.dependencies...)
}
func (c resourceComponent) Start(context.Context) error { return nil }
func (c resourceComponent) Health(context.Context) runtime.ComponentHealth {
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}
func (c resourceComponent) Stop(context.Context) error {
	if c.stop == nil {
		return nil
	}
	return c.stop()
}
