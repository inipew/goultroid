package runtime

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync/atomic"
)

const DefaultLifecycleCallbackConcurrency = 8

// CallbackPanicError preserves a recovered callback panic and stack without
// letting lifecycle worker goroutines crash the process.
type CallbackPanicError struct {
	Value any
	Stack []byte
}

func (e *CallbackPanicError) Error() string {
	if e == nil {
		return "callback panic"
	}
	return fmt.Sprintf("callback panic: %v", e.Value)
}

// CallbackExecutor bounds concurrently running lifecycle/cleanup callbacks.
// A callback that ignores cancellation may outlive its caller, but it keeps its
// slot until it actually exits, preventing repeated deadlines from spawning an
// unbounded number of orphan goroutines.
type CallbackExecutor struct {
	slots     chan struct{}
	active    atomic.Int64
	peak      atomic.Int64
	saturated atomic.Uint64
}

// CallbackExecutorStats is a lock-free diagnostics snapshot.
type CallbackExecutorStats struct {
	Capacity  int
	Active    int64
	Peak      int64
	Saturated uint64
}

func NewCallbackExecutor(concurrency int) *CallbackExecutor {
	if concurrency <= 0 {
		concurrency = DefaultLifecycleCallbackConcurrency
	}
	return &CallbackExecutor{slots: make(chan struct{}, concurrency)}
}

func (e *CallbackExecutor) updatePeak(active int64) {
	for {
		peak := e.peak.Load()
		if active <= peak || e.peak.CompareAndSwap(peak, active) {
			return
		}
	}
}

// Start acquires callback capacity and starts fn. The returned channel resolves
// only when fn really exits, even if the admission context is later cancelled.
func (e *CallbackExecutor) Start(ctx context.Context, fn func() error) (<-chan error, error) {
	return e.StartWithCompletion(ctx, fn, nil)
}

// StartWithCompletion is Start plus an in-goroutine completion hook. The hook
// receives the final normalized callback error, including recovered panics,
// before capacity is released. It lets lifecycle owners update bookkeeping
// without spawning a second goroutine just to wait on the result channel.
func (e *CallbackExecutor) StartWithCompletion(ctx context.Context, fn func() error, onDone func(error)) (<-chan error, error) {
	if fn == nil {
		return nil, errors.New("callback cannot be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e == nil {
		return nil, errors.New("callback executor is nil")
	}

	select {
	case e.slots <- struct{}{}:
	case <-ctx.Done():
		e.saturated.Add(1)
		return nil, ctx.Err()
	}

	active := e.active.Add(1)
	e.updatePeak(active)
	result := make(chan error, 1)
	go func() {
		var callbackErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				callbackErr = &CallbackPanicError{Value: recovered, Stack: debug.Stack()}
			}
			if onDone != nil {
				var completionErr error
				func() {
					defer func() {
						if recovered := recover(); recovered != nil {
							completionErr = &CallbackPanicError{Value: recovered, Stack: debug.Stack()}
						}
					}()
					onDone(callbackErr)
				}()
				if completionErr != nil {
					callbackErr = errors.Join(callbackErr, completionErr)
				}
			}
			e.active.Add(-1)
			<-e.slots
			result <- callbackErr
			close(result)
		}()
		callbackErr = fn()
	}()
	return result, nil
}

// Run executes fn under the bounded callback budget and waits until fn returns
// or ctx expires. Context expiry does not free capacity early: the callback
// keeps its slot until it really exits.
func (e *CallbackExecutor) Run(ctx context.Context, fn func() error) error {
	result, err := e.Start(ctx, fn)
	if err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *CallbackExecutor) Stats() CallbackExecutorStats {
	if e == nil {
		return CallbackExecutorStats{}
	}
	return CallbackExecutorStats{
		Capacity:  cap(e.slots),
		Active:    e.active.Load(),
		Peak:      e.peak.Load(),
		Saturated: e.saturated.Load(),
	}
}
