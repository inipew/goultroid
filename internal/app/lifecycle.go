package app

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
)

func (a *App) startBackgroundServices(ctx context.Context) error {
	if a.eventBus != nil { if err := a.eventBus.Start(ctx); err != nil { return fmt.Errorf("event bus: %w", err) } }
	if a.settingsService != nil { _ = a.settingsService.Start(ctx) }
	if a.callbackStore != nil { a.callbackStore.Start(ctx) }
	if a.inlineEngine != nil && a.inlineEngine.Cache() != nil { a.inlineEngine.Cache().Start(ctx) }
	if a.client != nil && a.client.Dispatcher() != nil { a.client.Dispatcher().SetRootContext(ctx); a.client.Dispatcher().Start(ctx) }
	if a.sched != nil {
		if a.client != nil && a.client.Ready() != nil {
			go func(){ select{case <-ctx.Done():return;case <-a.client.Ready():}; if err:=a.sched.Start(ctx);err!=nil&&!errors.Is(err,context.Canceled){a.logger.Warn("scheduler failed to start after Telegram readiness gate",zap.Error(err))} }()
		} else { go func(){if err:=a.sched.Start(ctx);err!=nil&&!errors.Is(err,context.Canceled){a.logger.Warn("scheduler stopped with error",zap.Error(err))}}() }
	}
	if a.assistant != nil { go func(){if err:=a.assistant.Start(ctx);err!=nil&&!errors.Is(err,context.Canceled){a.logger.Warn("assistant bot stopped with error",zap.Error(err))}}() }
	return nil
}

func (a *App) runLifecycle(ctx context.Context) error {
	if ctx == nil { ctx = context.Background() }
	if a.lifecycle == nil { a.lifecycle = newLifecycle() }
	if err := a.lifecycle.beginStart(); err != nil { return err }
	a.logger.Info("starting GoUltroid...")
	if err := a.startBackgroundServices(ctx); err != nil { a.lifecycle.markFailed(); return err }
	if a.client == nil { a.lifecycle.markFailed(); return fmt.Errorf("telegram client is nil") }
	a.lifecycle.markRunning()
	return a.client.Run(ctx)
}
