package app

import (
	"context"
	"errors"
	"fmt"
	"syscall"
)

// Shutdown releases long-lived application resources in strict dependency order.
func (a *App) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var errs []error

	// 1. Stop Telegram dispatcher: drain peer worker pool and stop new update handling
	if a.client != nil && a.client.Dispatcher() != nil {
		if err := a.client.Dispatcher().Stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errs = append(errs, fmt.Errorf("dispatcher: %w", err))
		}
	}

	// 2. Stop scheduler engine before stopping plugins and database
	if a.sched != nil {
		if err := a.sched.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("scheduler: %w", err))
		}
	}

	// 3. Stop addon external runtimes
	if a.addonMgr != nil {
		if err := a.addonMgr.ShutdownRuntimes(); err != nil {
			errs = append(errs, fmt.Errorf("addon runtimes: %w", err))
		}
	}

	// 4. Shutdown plugins
	if a.plugins != nil {
		if err := a.plugins.ShutdownWithContext(ctx); err != nil {
			errs = append(errs, fmt.Errorf("plugins: %w", err))
		}
	}

	// 5. Close event bus to drain asynchronous event workers
	if a.eventBus != nil {
		if err := a.eventBus.Close(); err != nil {
			errs = append(errs, fmt.Errorf("event bus: %w", err))
		}
	}

	// 6. Stop interaction state stores (callbacks & inline cache)
	if a.callbackStore != nil {
		a.callbackStore.Stop()
	}
	if a.inlineEngine != nil && a.inlineEngine.Cache() != nil {
		a.inlineEngine.Cache().Stop()
	}

	// 7. Close command rate limiter
	if a.limiter != nil {
		if err := a.limiter.Close(); err != nil {
			errs = append(errs, fmt.Errorf("rate limiter: %w", err))
		}
	}

	// 8. Close database connection only after all background consumers are stopped
	if a.db != nil {
		if err := a.db.Close(); err != nil {
			errs = append(errs, fmt.Errorf("database: %w", err))
		}
	}

	// 9. Flush logger
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
