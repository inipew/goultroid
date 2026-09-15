package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// startBackgroundServices starts long-lived services in dependency order. App lifecycle
// admission is controlled centrally; this function does not perform shutdown.
func (a *App) startBackgroundServices(ctx context.Context) error {
	appCtx, appCancel := context.WithCancel(context.Background())
	a.appCancel = appCancel

	if a.client != nil && a.client.Dispatcher() != nil {
		a.client.Dispatcher().SetRootContext(appCtx)
	}
	if a.runtime != nil {
		if err := a.runtime.Start(appCtx); err != nil {
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

	transportCtx, transportCancel := context.WithCancel(context.Background())
	a.transportCancel = transportCancel

	clientErrCh := make(chan error, 1)
	go func() {
		clientErrCh <- a.client.Run(transportCtx)
	}()

	select {
	case err := <-clientErrCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			a.logger.Error("telegram transport exited", zap.Error(err))
			return err
		}
		return nil
	case <-ctx.Done():
		quiesceCtx, quiesceCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if a.client != nil && a.client.Dispatcher() != nil {
			_ = a.client.Dispatcher().Quiesce(quiesceCtx)
		}
		quiesceCancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		shutdownErr := a.Shutdown(shutdownCtx)
		shutdownCancel()

		transportCancel()
		<-clientErrCh

		if shutdownErr != nil {
			return shutdownErr
		}
		return nil
	}
}
