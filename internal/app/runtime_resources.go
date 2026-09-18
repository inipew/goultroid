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

// ForceStop preserves emergency lifecycle capability across the composition
// wrapper. Components that do not expose an explicit forced path remain a
// no-op here; Runtime must never call an arbitrary potentially blocking Stop
// after the graceful budget has expired.
func (c dependencyComponent) ForceStop(ctx context.Context) error {
	if component, ok := c.Component.(runtime.ForcedStopper); ok {
		return component.ForceStop(ctx)
	}
	return nil
}

type resourceComponent struct {
	name         string
	dependencies []string
	start        func(context.Context) error
	stop         func() error
	stopContext  func(context.Context) error
}

func (c resourceComponent) Name() string { return c.name }
func (c resourceComponent) Dependencies() []string {
	return append([]string(nil), c.dependencies...)
}
func (c resourceComponent) Start(ctx context.Context) error {
	if c.start != nil {
		return c.start(ctx)
	}
	return nil
}
func (c resourceComponent) Health(context.Context) runtime.ComponentHealth {
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}
func (c resourceComponent) Stop(ctx context.Context) error {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if c.stopContext != nil {
		return c.stopContext(ctx)
	}
	if c.stop == nil {
		return nil
	}
	return c.stop()
}
