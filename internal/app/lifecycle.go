package app

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
)

// startBackgroundServices starts long-lived services in dependency order. App lifecycle
// admission is controlled centrally; this function does not perform shutdown.
func (a *App) startBackgroundServices(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if a.workers != nil {
		if err := a.workers.Start(ctx); err != nil {
			return fmt.Errorf("worker manager: %w", err)
		}
	}
	if a.eventBus != nil {
		if err := a.eventBus.Start(ctx); err != nil {
			return fmt.Errorf("event bus: %w", err)
		}
	}
	if a.settingsService != nil {
		a.settingsService.Start(ctx)
	}
	if a.callbackStore != nil {
		a.callbackStore.Start(ctx)
	}
	if a.inlineEngine != nil && a.inlineEngine.Cache() != nil {
		a.inlineEngine.Cache().Start(ctx)
	}
	if a.client != nil && a.client.Dispatcher() != nil {
		a.client.Dispatcher().SetRootContext(ctx)
		a.client.Dispatcher().Start(ctx)
	}
	if a.sched != nil {
		if a.client != nil && a.client.Ready() != nil {
			go func() {
				select {
				case <-ctx.Done():
					return
				case <-a.client.Ready():
				}
				if err := a.sched.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
					a.logger.Warn("scheduler failed to start after Telegram readiness gate", zap.Error(err))
				} else {
					a.logger.Info("scheduler engine started after Telegram readiness gate")
				}
			}()
		} else {
			go func() {
				if err := a.sched.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
					a.logger.Warn("scheduler engine stopped with error", zap.Error(err))
				}
			}()
		}
	}
	if a.assistant != nil {
		go func() {
			if err := a.assistant.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.logger.Warn("assistant bot stopped with error", zap.Error(err))
			}
		}()
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
