package app

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"
)

const appShutdownTimeout = 30 * time.Second

func (a *App) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if a.beginQuiesce() {
		go a.performShutdown()
	}
	if err := ctx.Err(); err != nil {
		return err
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
	// One App-owned deadline governs every teardown phase. External Shutdown
	// caller contexts only bound their own wait and never shorten this budget.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), appShutdownTimeout)
	defer shutdownCancel()

	quiesceCtx, cancelQuiesce := context.WithTimeout(shutdownCtx, 5*time.Second)
	if a.client != nil && a.client.Dispatcher() != nil {
		_ = a.client.Dispatcher().Quiesce(quiesceCtx)
	}
	cancelQuiesce()
	a.markStopping()
	if a.savedDeepLinkRegistration != nil {
		a.savedDeepLinkRegistration.Close()
	}

	var errs []error
	if a.runtime != nil {
		if err := a.runtime.StopWithin(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("runtime: %w", err))
		}
	}

	// Transport/RPC remains available throughout Runtime drain. Once runtime
	// teardown finishes (or consumes the global budget), cancel transport and
	// join only within the remaining global budget.
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
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			errs = append(errs, errors.New("telegram transport did not stop before join deadline"))
		case <-shutdownCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			errs = append(errs, fmt.Errorf("telegram transport join exceeded global shutdown deadline: %w", shutdownCtx.Err()))
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
