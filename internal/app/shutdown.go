package app

import (
	"context"
	"errors"
	"fmt"
	"syscall"
)

// Shutdown is the single teardown owner. It first transitions the application to
// Quiescing, which closes ingress admission before dependencies are stopped.
// Repeated/concurrent Shutdown calls wait for the first shutdown to finish.
func (a *App) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !a.beginQuiesce() {
		select {
		case <-a.shutdownDone:
			a.lifecycleMu.Lock()
			err := a.shutdownErr
			a.lifecycleMu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	a.markStopping()

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
		if err := a.plugins.ShutdownWithContext(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errs = append(errs, fmt.Errorf("plugins: %w", err))
		}
	}
	if a.eventBus != nil {
		if err := a.eventBus.Close(); err != nil {
			errs = append(errs, fmt.Errorf("event bus: %w", err))
		}
	}
	if a.workers != nil {
		if err := a.workers.Stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errs = append(errs, fmt.Errorf("workers: %w", err))
		}
	}
	if a.idemp != nil {
		a.idemp.Close()
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

	var shutdownErr error
	if len(errs) > 0 {
		shutdownErr = fmt.Errorf("shutdown completed with errors: %v", errs)
	}
	a.markStopped(shutdownErr)
	return shutdownErr
}
