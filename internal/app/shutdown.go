package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

const appShutdownTimeout = 30 * time.Second

type producerStopState uint8

const (
	producersStoppedCleanly producerStopState = iota
	producersTimedOutActive
)

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
	a.performShutdownWith(appShutdownTimeout, transportJoinTimeout, nil)
}

func (a *App) performShutdownWith(shutdownBudget, joinBudget time.Duration, errOut io.Writer) {
	// One App-owned deadline governs every teardown phase. External Shutdown
	// caller contexts only bound their own wait and never shorten this budget.
	if shutdownBudget <= 0 {
		shutdownBudget = appShutdownTimeout
	}
	if joinBudget <= 0 {
		joinBudget = transportJoinTimeout
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer shutdownCancel()

	a.markStopping()
	if a.savedDeepLinkRegistration != nil {
		a.savedDeepLinkRegistration.Close()
	}

	var errs []error
	producerState := producersStoppedCleanly

	if a.runtime != nil {
		if err := a.runtime.StopWithin(shutdownCtx); err != nil {
			producerState = producersTimedOutActive
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
		timer := time.NewTimer(joinBudget)
		select {
		case <-transportDone:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			producerState = producersTimedOutActive
			errs = append(errs, errors.New("telegram transport did not stop before join deadline"))
		case <-shutdownCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			producerState = producersTimedOutActive
			errs = append(errs, fmt.Errorf("telegram transport join exceeded global shutdown deadline: %w", shutdownCtx.Err()))
		}
	}
	if appCancel != nil {
		appCancel()
	}
	if a.resources != nil {
		if cleanupErrs := a.resources.ForceCleanupAllContext(shutdownCtx); len(cleanupErrs) > 0 {
			errs = append(errs, fmt.Errorf("resource cleanup: %w", errors.Join(cleanupErrs...)))
		}
		if leakErr := a.resources.VerifyShutdownClean(); leakErr != nil {
			errs = append(errs, leakErr)
		}
	}

	// Flush buffered logs once. When producers timed out and may still be active,
	// report the emergency-shutdown state explicitly via sanitized stderr fallback.
	if producerState == producersTimedOutActive {
		reportShutdownFallback(errOut, "emergency shutdown proceeding while producers remain active")
	}
	if err := syncAppLogger(a.opLogger, errOut); err != nil {
		errs = append(errs, fmt.Errorf("logger: %w", err))
	}

	var shutdownErr error
	if len(errs) > 0 {
		shutdownErr = fmt.Errorf("shutdown completed with errors: %w", errors.Join(errs...))
	}
	a.markStopped(shutdownErr)
}
