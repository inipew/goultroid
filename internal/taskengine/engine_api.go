package taskengine

import (
	"context"
	"errors"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func (e *Engine) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.AdmissionTicket, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestedAt := time.Now().UTC()
	response := make(chan submitResponse, 1)
	req := submitRequest{ctx: ctx, spec: spec, requestedAt: requestedAt, response: response}
	if err := e.sendBeforeLinearization(ctx, req); err != nil {
		return tasks.AdmissionTicket{}, err
	}
	result, ok := waitResponse(response, e.doneChannel())
	if !ok {
		return tasks.AdmissionTicket{}, ErrEngineNotRunning
	}
	return result.ticket, result.err
}

func (e *Engine) Cancel(taskID tasks.TaskID, reason tasks.CancelReason) (tasks.CancelReceipt, error) {
	response := make(chan cancelResponse, 1)
	if err := e.send(cancelRequest{taskID: taskID, reason: reason, response: response}); err != nil {
		return tasks.CancelReceipt{}, err
	}
	result, ok := waitResponse(response, e.doneChannel())
	if !ok {
		return tasks.CancelReceipt{}, ErrEngineNotRunning
	}
	return result.receipt, result.err
}

func (e *Engine) Snapshot(taskID tasks.TaskID) (tasks.TaskSnapshot, bool) {
	response := make(chan snapshotResponse, 1)
	if err := e.send(snapshotRequest{taskID: taskID, response: response}); err != nil {
		return tasks.TaskSnapshot{}, false
	}
	result, ok := waitResponse(response, e.doneChannel())
	if !ok {
		return tasks.TaskSnapshot{}, false
	}
	return result.snapshot, result.ok
}

// ConsumeResult releases the admission-time result credit exactly once while
// retaining the terminal task snapshot until ResultRetention expires.
func (e *Engine) ConsumeResult(taskID tasks.TaskID) (tasks.TaskResult, bool, error) {
	response := make(chan consumeResponse, 1)
	if err := e.send(consumeRequest{taskID: taskID, response: response}); err != nil {
		return tasks.TaskResult{}, false, err
	}
	result, ok := waitResponse(response, e.doneChannel())
	if !ok {
		return tasks.TaskResult{}, false, ErrEngineNotRunning
	}
	return result.result, result.ok, result.err
}

// CloseScope fences one exact lifecycle generation and requests cancellation
// for every accepted task owned by it. A later generation remains independent.
func (e *Engine) CloseScope(scope tasks.ScopeIdentity) (int, error) {
	if scope.IsZero() {
		return 0, errors.New("scope identity is required")
	}
	response := make(chan int, 1)
	if err := e.send(closeScopeRequest{scope: scope, response: response}); err != nil {
		return 0, err
	}
	count, ok := waitResponse(response, e.doneChannel())
	if !ok {
		return 0, ErrEngineNotRunning
	}
	return count, nil
}

func (e *Engine) Stats() Stats {
	response := make(chan Stats, 1)
	if err := e.send(statsRequest{response: response}); err != nil {
		return Stats{}
	}
	stats, ok := waitResponse(response, e.doneChannel())
	if !ok {
		return Stats{}
	}
	return stats
}

func (e *Engine) Quiesce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	response := make(chan struct{}, 1)
	if err := e.sendBeforeLinearization(ctx, quiesceRequest{response: response}); err != nil {
		return err
	}
	select {
	case <-response:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-e.doneChannel():
		return nil
	}
}

func (e *Engine) Drain(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	response := make(chan (<-chan struct{}), 1)
	if err := e.sendBeforeLinearization(ctx, drainRequest{response: response}); err != nil {
		return err
	}
	var settled <-chan struct{}
	select {
	case settled = <-response:
	case <-ctx.Done():
		return ctx.Err()
	case <-e.doneChannel():
		return nil
	}
	select {
	case <-settled:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-e.doneChannel():
		return nil
	}
}

// Stop is graceful: admission closes, accepted work drains, then the coordinator
// exits. If ctx expires, the engine remains alive so a later Stop can continue
// the drain instead of dropping worker results.
func (e *Engine) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := e.Quiesce(ctx); err != nil {
		return err
	}
	if err := e.Drain(ctx); err != nil {
		return err
	}

	e.mu.RLock()
	cancel := e.cancel
	done := e.done
	e.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) Started(permit tasks.PhysicalPermit, at time.Time) error {
	if permit.IsZero() || at.IsZero() {
		return errors.New("worker start event is invalid")
	}
	select {
	case e.startedEvents <- startedEvent{permit: permit, at: at}:
		return nil
	case <-e.doneChannel():
		return ErrEngineNotRunning
	}
}

func (e *Engine) Completed(permit tasks.PhysicalPermit, result tasks.TaskResult) error {
	if permit.IsZero() || result.TaskID() == "" {
		return errors.New("worker completion event is invalid")
	}
	select {
	case e.results <- completedEvent{permit: permit, result: result}:
		return nil
	case <-e.doneChannel():
		return ErrEngineNotRunning
	}
}

func (e *Engine) sendBeforeLinearization(ctx context.Context, message any) error {
	e.mu.RLock()
	running := e.running
	control := e.control
	done := e.done
	e.mu.RUnlock()
	if !running || done == nil {
		return ErrEngineNotRunning
	}
	select {
	case control <- message:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return ErrEngineNotRunning
	}
}

func (e *Engine) send(message any) error {
	e.mu.RLock()
	running := e.running
	control := e.control
	done := e.done
	e.mu.RUnlock()
	if !running || done == nil {
		return ErrEngineNotRunning
	}
	select {
	case control <- message:
		return nil
	case <-done:
		return ErrEngineNotRunning
	}
}

func (e *Engine) doneChannel() <-chan struct{} {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.done == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return e.done
}

func waitResponse[T any](response <-chan T, done <-chan struct{}) (T, bool) {
	select {
	case value := <-response:
		return value, true
	case <-done:
		// If coordinator committed a decision before shutdown, prefer that
		// decision over the lifecycle signal. This preserves admission
		// linearization even when both become ready concurrently.
		select {
		case value := <-response:
			return value, true
		default:
			var zero T
			return zero, false
		}
	}
}
