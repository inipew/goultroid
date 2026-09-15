package taskengine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
)

const forcedFinalizeReserve = 100 * time.Millisecond

var _ runtime.ForcedStopper = (*Engine)(nil)

// ForceStop fences admission and execution state without performing a
// graceful drain. The supplied context is a hard upper bound.
func (e *Engine) ForceStop(ctx context.Context) error {
	return e.stopEngine(ctx, false)
}

func reserveFinalizeBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.Background(), func() {}
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return ctx, func() {}
	}
	cutoff := deadline.Add(-forcedFinalizeReserve)
	if time.Until(cutoff) <= 0 {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, cutoff)
}

func (e *Engine) stopEngine(ctx context.Context, graceful bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var errs []error
	if graceful {
		drainCtx, cancelDrain := reserveFinalizeBudget(ctx)
		if err := e.Drain(drainCtx); err != nil {
			errs = append(errs, err)
		}
		cancelDrain()
	}

	e.mu.Lock()
	controlInbox := e.controlInbox
	rootCtx := e.rootCtx
	rootCancel := e.rootCancel
	delivery := e.delivery
	durability := e.durability
	runtimeDone := e.runtimeDone
	e.mu.Unlock()

	// Stop before Start is a no-op. In particular, do not stop ancillary lanes
	// or wait on runtimeDone: they have not been started and must remain usable
	// by a later Start call.
	if rootCtx == nil {
		return errors.Join(errs...)
	}

	if controlInbox != nil && rootCtx.Err() == nil {
		reply := make(chan engineReply, 1)
		req := engineRequest{op: opStopFinalize, reply: reply}
		sent := false
		select {
		case controlInbox <- req:
			sent = true
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("taskengine force-finalize enqueue: %w", ctx.Err()))
		case <-rootCtx.Done():
		}
		if sent {
			select {
			case <-reply:
			case <-ctx.Done():
				errs = append(errs, fmt.Errorf("taskengine force-finalize acknowledgement: %w", ctx.Err()))
			case <-rootCtx.Done():
			}
		}
	}

	if rootCancel != nil {
		rootCancel()
	}
	if runtimeDone != nil {
		select {
		case <-runtimeDone:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("taskengine execution loops stop: %w", ctx.Err()))
		}
	}
	if durability != nil {
		if err := durability.stop(ctx); err != nil && ctx.Err() == nil {
			errs = append(errs, fmt.Errorf("durability lane stop: %w", err))
		}
	}
	if delivery != nil {
		if err := delivery.stop(ctx); err != nil && ctx.Err() == nil {
			errs = append(errs, fmt.Errorf("completion delivery stop: %w", err))
		}
	}
	return errors.Join(errs...)
}
