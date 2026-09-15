package taskengine

import (
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func (e *Engine) cancelTask(taskID tasks.TaskID, reason tasks.CancelReason, now time.Time) (tasks.CancelReceipt, error) {
	if !validCancelReason(reason) {
		return tasks.CancelReceipt{}, errors.New("invalid cancellation reason")
	}
	record := e.records[taskID]
	if record == nil {
		return tasks.CancelReceipt{}, ErrTaskNotFound
	}
	if record.state.Terminal() {
		return tasks.CancelReceipt{TaskID: taskID, AlreadyTerminal: true, State: record.state}, nil
	}
	record.cancelled = true
	record.cancelReason = reason

	if record.state == tasks.LifecycleQueued {
		_, _ = e.ready.Remove(taskID)
		e.deadlines.Remove(taskID)
		e.leaveWaiting(record)
		cause := tasks.ResultCauseCancellation
		if reason == tasks.CancelScopeClosed {
			cause = tasks.ResultCauseScopeClosed
		}
		e.finishBeforeStart(record, tasks.OutcomeCancelled, cause, "cancelled", string(reason), now)
	} else if record.runCancel != nil {
		record.runCancel()
	}
	return tasks.CancelReceipt{TaskID: taskID, Requested: true, State: record.state}, nil
}

func validCancelReason(reason tasks.CancelReason) bool {
	switch reason {
	case tasks.CancelCaller, tasks.CancelScopeClosed, tasks.CancelShutdown, tasks.CancelSuperseded, tasks.CancelOperator:
		return true
	default:
		return false
	}
}

func (e *Engine) finishBeforeStart(record *taskRecord, outcome tasks.Outcome, cause tasks.ResultCause, code, message string, now time.Time) {
	result, err := tasks.NewTaskResult(tasks.TaskResultParams{
		TaskID: record.spec.ID(), Outcome: outcome, Cause: cause, FinishedAt: now,
		Failure: tasks.FailureInfo{Code: code, Message: message},
	})
	if err != nil {
		panic(fmt.Sprintf("taskengine: construct terminal result: %v", err))
	}
	record.finishedAt = now
	record.state = lifecycleForOutcome(outcome)
	record.result = &result
	e.scheduleRetention(record, now)
}

func (e *Engine) scheduleRetention(record *taskRecord, now time.Time) {
	record.retireAt = now.Add(e.cfg.ResultRetention)
	_ = e.retentions.Upsert(record.spec.ID(), record.retireAt)
}

func (e *Engine) leaveWaiting(record *taskRecord) {
	payloadBytes := int64(record.spec.Input().Size())
	pool := e.pools[record.spec.Pool()]
	if pool.waiting > 0 {
		pool.waiting--
	}
	if pool.waitingBytes >= payloadBytes {
		pool.waitingBytes -= payloadBytes
	} else {
		pool.waitingBytes = 0
	}
	owner := e.owner(record.spec.QuotaOwner())
	if owner.waiting > 0 {
		owner.waiting--
	}
	if owner.waitingBytes >= payloadBytes {
		owner.waitingBytes -= payloadBytes
	} else {
		owner.waitingBytes = 0
	}
	e.deleteOwnerIfIdle(record.spec.QuotaOwner())
}

func (e *Engine) ownerLimits(id tasks.QuotaOwner) OwnerLimits {
	if limits, ok := e.cfg.Owners[id]; ok {
		return limits
	}
	return e.cfg.DefaultOwner
}

func (e *Engine) owner(id tasks.QuotaOwner) *ownerUsage {
	usage := e.owners[id]
	if usage == nil {
		usage = &ownerUsage{}
		e.owners[id] = usage
	}
	return usage
}

func (e *Engine) deleteOwnerIfIdle(id tasks.QuotaOwner) {
	usage := e.owners[id]
	if usage == nil {
		return
	}
	if usage.waiting == 0 && usage.waitingBytes == 0 && usage.reserved == 0 && usage.running == 0 {
		delete(e.owners, id)
	}
}

func (e *Engine) snapshot(taskID tasks.TaskID) (tasks.TaskSnapshot, bool) {
	record := e.records[taskID]
	if record == nil {
		return tasks.TaskSnapshot{}, false
	}
	snapshot, err := tasks.NewTaskSnapshot(tasks.TaskSnapshotParams{
		ID: record.spec.ID(), State: record.state, Scope: record.spec.Scope(), QuotaOwner: record.spec.QuotaOwner(), Pool: record.spec.Pool(),
		CreatedAt: record.createdAt, AdmittedAt: record.admittedAt, QueuedAt: record.queuedAt, DispatchingAt: record.dispatchingAt,
		StartedAt: record.startedAt, FinishedAt: record.finishedAt, CancelRequested: record.cancelled,
	})
	if err != nil {
		return tasks.TaskSnapshot{}, false
	}
	return snapshot, true
}

func (e *Engine) consumeResult(taskID tasks.TaskID) (tasks.TaskResult, bool, error) {
	record := e.records[taskID]
	if record == nil {
		return tasks.TaskResult{}, false, ErrTaskNotFound
	}
	if !record.state.Terminal() || record.result == nil {
		return tasks.TaskResult{}, false, nil
	}
	if record.creditHeld {
		record.creditHeld = false
		if e.resultCreditsUsed > 0 {
			e.resultCreditsUsed--
		}
	}
	return *record.result, true, nil
}

func (e *Engine) nextTimerWait(now time.Time) (time.Duration, bool) {
	var next time.Time
	if _, deadline, ok := e.deadlines.Peek(); ok {
		next = deadline
	}
	if _, deadline, ok := e.retentions.Peek(); ok && (next.IsZero() || deadline.Before(next)) {
		next = deadline
	}
	if next.IsZero() {
		return 0, false
	}
	return next.Sub(now), true
}

func (e *Engine) expireDue(now time.Time, budget int) {
	for processed := 0; processed < budget; processed++ {
		taskID, _, ok := e.deadlines.PopDue(now)
		if !ok {
			break
		}
		record := e.records[taskID]
		if record == nil || record.state != tasks.LifecycleQueued {
			continue
		}
		_, _ = e.ready.Remove(taskID)
		e.leaveWaiting(record)
		e.finishBeforeStart(record, tasks.OutcomeExpired, tasks.ResultCauseQueueDeadline, "queue_deadline", "queue deadline expired", now)
	}
	for processed := 0; processed < budget; processed++ {
		taskID, _, ok := e.retentions.PopDue(now)
		if !ok {
			break
		}
		record := e.records[taskID]
		if record == nil || !record.state.Terminal() {
			continue
		}
		if record.creditHeld {
			record.creditHeld = false
			if e.resultCreditsUsed > 0 {
				e.resultCreditsUsed--
			}
		}
		delete(e.records, taskID)
	}
}

func (e *Engine) stats() Stats {
	stats := Stats{
		Accepting: e.accepting, Tasks: len(e.records), ResultCreditsUsed: e.resultCreditsUsed, ResultCapacity: e.cfg.ResultCredits,
		CommitPending: 0, ReadyQueued: e.ready.Len(), QueueDeadlines: e.deadlines.Len(), RetentionEntries: e.retentions.Len(),
		OrderingLocks: len(e.ordering), ClosedScopeOwners: len(e.closedScopes),
		Pools: make(map[tasks.PoolID]PoolStats, len(e.cfg.Pools)), Owners: make(map[tasks.QuotaOwner]OwnerStats, len(e.owners)), Resources: make(map[string]ResourceStats, len(e.cfg.ResourceCapacity)),
	}
	for pool, usage := range e.pools {
		poolStats := PoolStats{Workers: e.cfg.Pools[pool].Workers, Waiting: usage.waiting, WaitingBytes: usage.waitingBytes}
		for _, slot := range e.poolSlots[pool] {
			switch slot.phase {
			case slotIdle:
				poolStats.Idle++
			case slotReserved:
				poolStats.Reserved++
			case slotAssigned:
				poolStats.Assigned++
			case slotRunning:
				poolStats.Running++
			}
		}
		stats.Pools[pool] = poolStats
	}
	for owner, usage := range e.owners {
		stats.Owners[owner] = OwnerStats{
			Waiting: usage.waiting, WaitingBytes: usage.waitingBytes,
			Reserved: usage.reserved, Running: usage.running, Active: usage.reserved,
		}
	}
	for name, capacity := range e.cfg.ResourceCapacity {
		stats.Resources[name] = ResourceStats{Capacity: capacity, Used: e.resources[name]}
	}
	return stats
}

func (e *Engine) unsettledCount() int {
	count := 0
	for _, record := range e.records {
		if !record.state.Terminal() {
			count++
		}
	}
	return count
}

func (e *Engine) notifyDrainedIfSettled() {
	if e.unsettledCount() != 0 || len(e.drainWaiters) == 0 {
		return
	}
	for _, waiter := range e.drainWaiters {
		close(waiter)
	}
	e.drainWaiters = nil
}

func (e *Engine) cancelAcceptedForShutdown() {
	now := e.clock.Now().UTC()
	ids := make([]tasks.TaskID, 0, len(e.records))
	for id, record := range e.records {
		if !record.state.Terminal() {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		_, _ = e.cancelTask(id, tasks.CancelShutdown, now)
	}
}

func (e *Engine) releaseAllRetainedCredits() {
	for id, record := range e.records {
		if record.creditHeld {
			record.creditHeld = false
			if e.resultCreditsUsed > 0 {
				e.resultCreditsUsed--
			}
		}
		delete(e.records, id)
	}
}
