package app

import (
	"context"
	"errors"
	"fmt"
	"syscall"
)

func (a *App) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if a.lifecycle == nil {
		a.lifecycle = newLifecycle()
	}
	return a.lifecycle.shutdown(ctx, a.shutdownResources)
}

func (a *App) shutdownResources(ctx context.Context) error {
	var errs []error
	if a.assistant != nil {
		if err := a.assistant.Stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errs = append(errs, fmt.Errorf("assistant: %w", err))
		}
	}
	if a.client != nil && a.client.Dispatcher() != nil {
		if err := a.client.Dispatcher().Stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errs = append(errs, fmt.Errorf("dispatcher: %w", err))
		}
	}
	if a.sched != nil {
		if err := a.sched.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("scheduler: %w", err))
		}
	}
	if a.settingsService != nil {
		a.settingsService.Stop()
	}
	if a.addonMgr != nil {
		if err := a.addonMgr.ShutdownRuntimes(); err != nil {
			errs = append(errs, fmt.Errorf("addon runtimes: %w", err))
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
	if a.callbackStore != nil {
		a.callbackStore.Stop()
	}
	if a.inlineEngine != nil && a.inlineEngine.Cache() != nil {
		a.inlineEngine.Cache().Stop()
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
		if err := a.logger.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
			errs = append(errs, fmt.Errorf("logger: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("shutdown completed with errors: %v", errs)
	}
	return nil
}
