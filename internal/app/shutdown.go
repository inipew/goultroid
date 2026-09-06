package app

import (
	"context"
	"fmt"
)

// Shutdown releases long-lived application resources in dependency order.
func (a *App) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var errs []error

	if a.sched != nil {
		if err := a.sched.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("scheduler: %w", err))
		}
	}
	if a.plugins != nil {
		if err := a.plugins.ShutdownWithContext(ctx); err != nil {
			errs = append(errs, fmt.Errorf("plugins: %w", err))
		}
	}
	if a.eventBus != nil {
		if err := a.eventBus.Close(); err != nil {
			errs = append(errs, fmt.Errorf("event bus: %w", err))
		}
	}
	if a.limiter != nil {
		if err := a.limiter.Close(); err != nil {
			errs = append(errs, fmt.Errorf("rate limiter: %w", err))
		}
	}
	if a.db != nil {
		if err := a.db.Close(); err != nil {
			errs = append(errs, fmt.Errorf("database: %w", err))
		}
	}
	if a.logger != nil {
		if err := a.logger.Sync(); err != nil {
			errs = append(errs, fmt.Errorf("logger: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("shutdown completed with errors: %v", errs)
	}
	return nil
}
