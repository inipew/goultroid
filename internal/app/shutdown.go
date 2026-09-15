package app

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"
)

func (a *App) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if a.beginQuiesce() {
		go a.performShutdown()
	}
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

func (a *App) performShutdown() {
	quiesceCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if a.client != nil && a.client.Dispatcher() != nil {
		_ = a.client.Dispatcher().Quiesce(quiesceCtx)
	}
	cancel()
	a.markStopping()

	var errs []error
	if a.runtime != nil {
		// Runtime owns its own bounded stop deadline. Do not couple actual
		// teardown to the first caller's wait context.
		if err := a.runtime.Stop(context.Background()); err != nil {
			errs = append(errs, fmt.Errorf("runtime: %w", err))
		}
	}

	// Transport/RPC remains available throughout Runtime drain. The published
	// cancel function also owns a bounded join, so no client.Run goroutine is
	// waited on indefinitely after the execution/runtime layers have stopped.
	a.lifecycleMu.Lock()
	transportCancel := a.transportCancel
	transportDone := a.transportDone
	appCancel := a.appCancel
	a.lifecycleMu.Unlock()
	if transportCancel != nil {
		transportCancel()
	}
	if transportDone != nil {
		timer := time.NewTimer(transportJoinTimeout)
		select {
		case <-transportDone:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			errs = append(errs, errors.New("telegram transport did not stop before join deadline"))
		}
	}
	if appCancel != nil {
		appCancel()
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
}
