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
	if a.client != nil && a.client.Dispatcher() != nil {
		_ = a.client.Dispatcher().Quiesce(ctx)
	}
	if a.taskEngine != nil {
		_ = a.taskEngine.Quiesce(ctx)
	}
	a.markStopping()

	var errs []error
	if a.runtime != nil {
		if err := a.runtime.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("runtime: %w", err))
		}
	}
	if a.transportCancel != nil {
		a.transportCancel()
	}
	if a.appCancel != nil {
		a.appCancel()
	}
	if a.logger != nil {
		if err := a.logger.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
			errs = append(errs, fmt.Errorf("logger: %w", err))
		}
	}

	var shutdownErr error
	if len(errs) > 0 {
		shutdownErr = fmt.Errorf("shutdown completed with errors: %w", errors.Join(errs...))
	}
	a.markStopped(shutdownErr)
	return shutdownErr
}
