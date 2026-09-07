package app

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
)

// startBackgroundServices starts all long-lived background loops bound to the application root context.
func (a *App) startBackgroundServices(ctx context.Context) {
	// 1. Interaction state stores
	if a.callbackStore != nil {
		a.callbackStore.Start(ctx)
	}
	if a.inlineEngine != nil && a.inlineEngine.Cache() != nil {
		a.inlineEngine.Cache().Start(ctx)
	}

	// 2. Telegram dispatcher worker pool
	if a.client != nil && a.client.Dispatcher() != nil {
		a.client.Dispatcher().SetRootContext(ctx)
		a.client.Dispatcher().Start(ctx)
	}

	// 3. Scheduler engine (readiness-gated to avoid claiming jobs before MTProto is connected)
	if a.sched != nil {
		if a.client != nil && a.client.Ready() != nil {
			go func() {
				select {
				case <-ctx.Done():
					return
				case <-a.client.Ready():
				}
				if err := a.sched.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
					a.logger.Warn("scheduler failed to start after readiness gate", zap.Error(err))
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

	// 4. Assistant bot client
	if a.assistant != nil {
		go func() {
			if err := a.assistant.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.logger.Warn("assistant bot stopped with error", zap.Error(err))
			}
		}()
	}
}

// runLifecycle coordinates application execution and teardown of runtime background tasks.
func (a *App) runLifecycle(ctx context.Context) error {
	a.logger.Info("starting GoUltroid...")

	// Register teardown hooks for background caches/state
	if a.callbackStore != nil {
		defer a.callbackStore.Stop()
	}
	if a.inlineEngine != nil && a.inlineEngine.Cache() != nil {
		defer a.inlineEngine.Cache().Stop()
		defer func() { _ = a.inlineEngine.Cache().Prune() }()
	}

	// Start all background workers with the root context
	a.startBackgroundServices(ctx)

	// Block on Telegram network client
	if a.client == nil {
		return fmt.Errorf("telegram client is nil")
	}
	return a.client.Run(ctx)
}
