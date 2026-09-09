package app

import (
	"context"
	"errors"
	"fmt"

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

type infrastructureComponent struct {
	core   *coreDependencies
	domain *domainServices
}

func (c *infrastructureComponent) Name() string           { return "infrastructure" }
func (c *infrastructureComponent) Dependencies() []string { return nil }
func (c *infrastructureComponent) Start(context.Context) error {
	return nil
}
func (c *infrastructureComponent) Health(context.Context) runtime.ComponentHealth {
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}
func (c *infrastructureComponent) Stop(context.Context) error {
	var errs []error
	if c.domain != nil && c.domain.addonManager != nil {
		if err := c.domain.addonManager.ShutdownRuntimes(); err != nil {
			errs = append(errs, fmt.Errorf("addon runtimes: %w", err))
		}
	}
	if c.core != nil {
		if c.core.cmdLimiter != nil {
			if err := c.core.cmdLimiter.Close(); err != nil {
				errs = append(errs, fmt.Errorf("command rate limiter: %w", err))
			}
		}
		if c.core.interLimiter != nil {
			if err := c.core.interLimiter.Close(); err != nil {
				errs = append(errs, fmt.Errorf("interaction rate limiter: %w", err))
			}
		}
		if c.core.idempManager != nil {
			c.core.idempManager.Close()
		}
		if c.core.db != nil {
			if err := c.core.db.Close(); err != nil {
				errs = append(errs, fmt.Errorf("database: %w", err))
			}
		}
	}
	return errors.Join(errs...)
}
