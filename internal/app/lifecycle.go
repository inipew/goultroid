package app

import (
	"context"
	"fmt"
)

// startBackgroundServices starts long-lived services in dependency order. App lifecycle
// admission is controlled centrally; this function does not perform shutdown.
func (a *App) startBackgroundServices(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if a.client != nil && a.client.Dispatcher() != nil {
		a.client.Dispatcher().SetRootContext(ctx)
	}
	if a.runtime != nil {
		if err := a.runtime.Start(ctx); err != nil {
			return fmt.Errorf("runtime: %w", err)
		}
	}
	return nil
}

// runLifecycle is the single startup owner. Shutdown is deliberately separate so
// cancellation can first quiesce ingress and then drain dependencies in reverse order.
func (a *App) runLifecycle(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.beginStart(); err != nil {
		return err
	}
	a.logger.Info("starting GoUltroid...")
	if err := a.startBackgroundServices(ctx); err != nil {
		a.lifecycleMu.Lock()
		a.lifecycleState.Store(uint32(lifecycleFailed))
		a.lifecycleMu.Unlock()
		return err
	}
	a.markRunning()
	if a.client == nil {
		a.lifecycleMu.Lock()
		a.lifecycleState.Store(uint32(lifecycleFailed))
		a.lifecycleMu.Unlock()
		return fmt.Errorf("telegram client is nil")
	}
	return a.client.Run(ctx)
}
