package taskengine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
)

// Health probes the health status of the task engine.
// Stats returns aggregate execution diagnostics through the coordinator so the
// snapshot is internally consistent.
func (e *Engine) Stats(ctx context.Context) (RuntimeStats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rep, err := e.sendControl(ctx, engineRequest{op: opStats, reply: make(chan engineReply, 1)})
	if err != nil {
		return RuntimeStats{}, err
	}
	s := rep.stats
	stats := RuntimeStats{
		ResultSlotsHeld: s.resultSlotsHeld, ResultCapacity: s.resultCapacity,
		ActiveTasks: s.activeTasks, RetainedBytes: s.retainedBytes, RetainedCap: s.retainedCap,
		TerminalCount: s.terminalCount, CommitPending: s.commitPending,
		DeliveryQueued: s.deliveryQueued, DeliveryCap: s.deliveryCap, DeliveryFailed: s.deliveryFailed,
		DurabilityQueue: s.durabilityQueue, DurabilityCap: s.durabilityCap, DurabilityFail: s.durabilityFail,
		ScopeTombstones: s.scopeTombstones,
		Pools:           s.pools, Resources: s.resources,
	}
	if e.delivery != nil {
		stats.DeliveryLane = LaneRuntimeStats{
			WorkerLimit: e.delivery.workers,
			Workers:     int(e.delivery.remaining.Load()),
			Pending:     int(e.delivery.pending.Load()),
			Active:      int(e.delivery.active.Load()),
		}
	}
	if e.durability != nil {
		stats.DurabilityLane = LaneRuntimeStats{
			WorkerLimit: e.durability.workers,
			Workers:     int(e.durability.remaining.Load()),
			Pending:     int(e.durability.pending.Load()),
			Active:      int(e.durability.active.Load()),
		}
	}
	return stats, nil
}

func (e *Engine) Health(ctx context.Context) runtime.ComponentHealth {
	if ctx == nil {
		ctx = context.Background()
	}
	healthCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	rep, err := e.sendControl(healthCtx, engineRequest{op: opStats})
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "health check cancelled"}
		case errors.Is(err, tasks.ErrEngineQuiescing):
			return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "task engine not running"}
		default:
			return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "task engine control loop unresponsive"}
		}
	}
	if rep.stats.resultCapacity > 0 && rep.stats.resultSlotsHeld >= rep.stats.resultCapacity {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: fmt.Sprintf("result capacity saturated (%d/%d)", rep.stats.resultSlotsHeld, rep.stats.resultCapacity)}
	}
	if rep.stats.retainedCap > 0 && rep.stats.retainedBytes >= rep.stats.retainedCap {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: fmt.Sprintf("retained memory saturated (%d/%d bytes)", rep.stats.retainedBytes, rep.stats.retainedCap)}
	}
	if rep.stats.deliveryCap > 0 && rep.stats.deliveryQueued >= rep.stats.deliveryCap {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: fmt.Sprintf("completion delivery saturated (%d/%d)", rep.stats.deliveryQueued, rep.stats.deliveryCap)}
	}
	if rep.stats.deliveryFailed > 0 {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: fmt.Sprintf("completion delivery invariant failures: %d", rep.stats.deliveryFailed)}
	}
	if rep.stats.durabilityCap > 0 && rep.stats.durabilityQueue >= rep.stats.durabilityCap {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: fmt.Sprintf("durability acknowledgement lane saturated (%d/%d)", rep.stats.durabilityQueue, rep.stats.durabilityCap)}
	}
	if rep.stats.durabilityFail > 0 {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: fmt.Sprintf("durability lane failures: %d", rep.stats.durabilityFail)}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}
// Cancel cancels an execution attempt by ID.
func (e *Engine) Cancel(id tasks.TaskID, reason tasks.Cause) (tasks.CancelReceipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.decisionTimeoutOrDefault())
	defer cancel()
	rep, err := e.sendControl(ctx, engineRequest{op: opCancel, taskID: id, reason: reason})
	if err != nil {
		return tasks.CancelReceipt{TaskID: id, Accepted: false, Reason: reason}, err
	}
	return rep.receipt, rep.err
}

// CancelScope cancels all active and queued tasks matching a scope and closes it.
func (e *Engine) CancelScope(scope tasks.ScopeIdentity, reason tasks.Cause) int {
	ctx, cancel := context.WithTimeout(context.Background(), e.decisionTimeoutOrDefault())
	defer cancel()
	rep, err := e.sendControl(ctx, engineRequest{op: opCancelScope, scope: scope, reason: reason})
	if err != nil {
		return 0
	}
	return rep.count
}

// Snapshot returns point-in-time lifecycle status of a task.
func (e *Engine) Snapshot(id tasks.TaskID) (tasks.TaskSnapshot, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), e.decisionTimeoutOrDefault())
	defer cancel()
	rep, err := e.sendControl(ctx, engineRequest{op: opSnapshot, taskID: id})
	if err != nil {
		return tasks.TaskSnapshot{}, false
	}
	return rep.snapshot, rep.found
}

func (e *Engine) decisionTimeoutOrDefault() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.decisionTimeout > 0 {
		return e.decisionTimeout
	}
	return 5 * time.Second
}

func (e *Engine) taskState(id tasks.TaskID) tasks.TaskState {
	snap, ok := e.Snapshot(id)
	if !ok {
		return ""
	}
	return snap.State
}

func (e *Engine) taskResult(id tasks.TaskID) (tasks.TaskResult, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rep, err := e.sendControl(ctx, engineRequest{op: opResult, taskID: id})
	if err != nil || !rep.hasResult {
		return tasks.TaskResult{}, false
	}
	return rep.result, true
}
