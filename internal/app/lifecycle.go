package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
)

const transportJoinTimeout = 5 * time.Second

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
			appCancel()
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
	if a.client == nil {
		err := errors.New("telegram client is nil")
		shutdownErr := a.Shutdown(context.Background())
		return errors.Join(err, shutdownErr)
	}

	transportCtx, rawTransportCancel := context.WithCancel(context.Background())
	transportDone := make(chan struct{})
	clientErrCh := make(chan error, 1)

	// Publish the transport owner and transition Starting -> Running atomically.
	// If shutdown won the race while Runtime was starting, never start a new
	// transport after ingress has already been quiesced.
	a.lifecycleMu.Lock()
	if lifecycleState(a.lifecycleState.Load()) != lifecycleStarting {
		a.lifecycleMu.Unlock()
		rawTransportCancel()
		return a.Shutdown(context.Background())
	}
	a.transportCancel = rawTransportCancel
	a.transportDone = transportDone
	a.lifecycleState.Store(uint32(lifecycleRunning))
	a.lifecycleMu.Unlock()

	go func() {
		defer close(transportDone)
		clientErrCh <- a.client.Run(transportCtx)
	}()

	select {
	case transportErr := <-clientErrCh:
		// Transport is part of App ownership even though it is intentionally not
		// a Runtime component. Any transport exit therefore tears down Runtime
		// before Run returns; otherwise DB workers/schedulers would outlive App.
		shutdownErr := a.Shutdown(context.Background())
		if transportErr != nil && !errors.Is(transportErr, context.Canceled) {
			a.logger.Error("telegram transport exited", zap.Error(transportErr))
			return errors.Join(transportErr, shutdownErr)
		}
		return shutdownErr
	case <-ctx.Done():
		// Shutdown owns transport cancellation and keeps RPC alive throughout
		// Runtime drain. Its transport join is internally bounded, so Run never
		// performs an unbounded receive after the caller has cancelled.
		return a.Shutdown(context.Background())
	}
}
