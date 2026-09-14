package taskengine

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/tasks"
)

func (e *Engine) dispatchAvailable() {
	now := time.Now().UTC()
	for _, poolID := range e.poolOrder {
		for {
			slot := e.nextIdleSlot(poolID)
			if slot == nil {
				break
			}
			item, ok := e.ready.Next(poolID, func(item admission.Item) bool {
				record := e.records[item.TaskID]
				return record != nil && record.state == tasks.LifecycleQueued && e.dispatchEligible(record)
			})
			if !ok {
				break
			}
			record := e.records[item.TaskID]
			e.deadlines.Remove(item.TaskID)
			e.leaveWaiting(record)
			e.reserveLogical(record)

			e.dispatchEpoch++
			permit, err := tasks.NewPhysicalPermit(poolID, slot.slot.WorkerID, slot.slot.Generation, record.spec.ID(), e.dispatchEpoch)
			if err != nil {
				e.releaseLogical(record)
				e.finishBeforeStart(record, tasks.OutcomeAbortedBeforeStart, tasks.ResultCauseInvalidPermit, "invalid_permit", err.Error(), now)
				continue
			}
			spec := record.spec
			if spec.ExecutionTimeout() == 0 {
				spec, err = spec.WithExecutionTimeout(e.cfg.ExecutionTimeout)
				if err != nil {
					e.releaseLogical(record)
					e.finishBeforeStart(record, tasks.OutcomeAbortedBeforeStart, tasks.ResultCauseInvalidPermit, "invalid_timeout", err.Error(), now)
					continue
				}
			}
			assignment, err := tasks.NewWorkerAssignment(permit, spec)
			if err != nil {
				e.releaseLogical(record)
				e.finishBeforeStart(record, tasks.OutcomeAbortedBeforeStart, tasks.ResultCauseInvalidPermit, "invalid_assignment", err.Error(), now)
				continue
			}

			slot.phase = slotReserved
			slot.task = record.spec.ID()
			record.permit = permit
			record.state = tasks.LifecycleDispatching
			record.dispatchingAt = now
			runCtx, runCancel := context.WithCancel(e.ctx)
			record.runCancel = runCancel

			slot.phase = slotAssigned
			if err := e.workers.Assign(runCtx, assignment, e); err != nil {
				runCancel()
				record.runCancel = nil
				slot.phase = slotIdle
				slot.task = ""
				e.releaseLogical(record)
				e.finishBeforeStart(record, tasks.OutcomeAbortedBeforeStart, tasks.ResultCauseInvalidPermit, "assignment_failed", err.Error(), time.Now().UTC())
				continue
			}
		}
	}
}

func (e *Engine) dispatchEligible(record *taskRecord) bool {
	owner := e.owner(record.spec.QuotaOwner())
	if owner.active >= e.ownerLimits(record.spec.QuotaOwner()).MaxActive {
		return false
	}
	if key := record.spec.OrderingKey(); key != "" {
		if _, held := e.ordering[key]; held {
			return false
		}
	}

	// Aggregate duplicate resource entries before comparing with the shared
	// inventory. Catalog validation guarantees aggregate demand fits the static
	// capacity; this check adds current usage without uint32 overflow.
	required := make(map[string]uint64, len(record.spec.Resources()))
	for _, request := range record.spec.Resources() {
		required[request.Name()] += uint64(request.Units())
	}
	for name, units := range required {
		capacity := uint64(e.cfg.ResourceCapacity[name])
		used := uint64(e.resources[name])
		if units > capacity || used > capacity-units {
			return false
		}
	}
	return true
}

func (e *Engine) reserveLogical(record *taskRecord) {
	owner := e.owner(record.spec.QuotaOwner())
	owner.active++
	if key := record.spec.OrderingKey(); key != "" {
		e.ordering[key] = record.spec.ID()
	}
	for _, request := range record.spec.Resources() {
		e.resources[request.Name()] += request.Units()
	}
}

func (e *Engine) releaseLogical(record *taskRecord) {
	owner := e.owner(record.spec.QuotaOwner())
	if owner.active > 0 {
		owner.active--
	}
	if key := record.spec.OrderingKey(); key != "" {
		if holder, held := e.ordering[key]; held && holder == record.spec.ID() {
			delete(e.ordering, key)
		}
	}
	for _, request := range record.spec.Resources() {
		used := e.resources[request.Name()]
		if used <= request.Units() {
			delete(e.resources, request.Name())
		} else {
			e.resources[request.Name()] = used - request.Units()
		}
	}
}

func (e *Engine) nextIdleSlot(pool tasks.PoolID) *slotRecord {
	slots := e.poolSlots[pool]
	if len(slots) == 0 {
		return nil
	}
	start := e.slotCursor[pool] % len(slots)
	for offset := 0; offset < len(slots); offset++ {
		index := (start + offset) % len(slots)
		if slots[index].phase == slotIdle {
			e.slotCursor[pool] = (index + 1) % len(slots)
			return slots[index]
		}
	}
	return nil
}

func (e *Engine) handleStarted(event startedEvent) {
	record := e.records[event.permit.TaskID()]
	if record == nil || !samePermit(record.permit, event.permit) {
		return
	}
	if record.state != tasks.LifecycleDispatching {
		return
	}
	slot := e.slotByID[event.permit.WorkerID()]
	if slot == nil || slot.task != record.spec.ID() || slot.phase != slotAssigned {
		return
	}
	record.state = tasks.LifecycleRunning
	record.startedAt = event.at
	slot.phase = slotRunning
}

func (e *Engine) handleCompleted(event completedEvent) {
	record := e.records[event.permit.TaskID()]
	if record == nil || !samePermit(record.permit, event.permit) || event.result.TaskID() != record.spec.ID() {
		return
	}
	if record.state != tasks.LifecycleDispatching && record.state != tasks.LifecycleRunning {
		return
	}
	if record.state == tasks.LifecycleDispatching && !event.result.StartedAt().IsZero() {
		record.startedAt = event.result.StartedAt()
	}
	if record.runCancel != nil {
		record.runCancel()
		record.runCancel = nil
	}
	slot := e.slotByID[event.permit.WorkerID()]
	if slot != nil && slot.task == record.spec.ID() {
		slot.phase = slotIdle
		slot.task = ""
	}
	e.releaseLogical(record)
	record.finishedAt = event.result.FinishedAt()
	result := event.result
	record.result = &result
	record.state = lifecycleForOutcome(event.result.Outcome())
	e.scheduleRetention(record, time.Now().UTC())
}

func lifecycleForOutcome(outcome tasks.Outcome) tasks.LifecycleState {
	switch outcome {
	case tasks.OutcomeSucceeded:
		return tasks.LifecycleSucceeded
	case tasks.OutcomeFailed:
		return tasks.LifecycleFailed
	case tasks.OutcomeTimedOut:
		return tasks.LifecycleTimedOut
	case tasks.OutcomeCancelled:
		return tasks.LifecycleCancelled
	case tasks.OutcomeExpired:
		return tasks.LifecycleExpired
	case tasks.OutcomeAbortedBeforeStart:
		return tasks.LifecycleAbortedBeforeStart
	default:
		return tasks.LifecycleFailed
	}
}

func samePermit(left, right tasks.PhysicalPermit) bool {
	return left.Pool() == right.Pool() && left.WorkerID() == right.WorkerID() && left.WorkerGeneration() == right.WorkerGeneration() && left.TaskID() == right.TaskID() && left.DispatchEpoch() == right.DispatchEpoch()
}
