package taskengine

import (
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
)

func (e *Engine) syncActiveTasks(delta int) { e.activeTasks += delta }

// boundResult enforces output/failure caps on a terminal result.
func (e *Engine) boundResult(res tasks.TaskResult) (tasks.TaskResult, int64) {
	out, outBytes := capOutput(res.Output, e.maxOutputBytes)
	res.Output = out
	res.Failure.Code = truncateField(res.Failure.Code, e.maxFailureBytes)
	res.Failure.Message = truncateField(res.Failure.Message, e.maxFailureBytes)
	res.Failure.Detail = truncateField(res.Failure.Detail, e.maxFailureBytes)
	return res, outBytes + int64(len(res.Disposition)+len(res.Failure.Code)+len(res.Failure.Message)+len(res.Failure.Detail))
}

// settleTerminal performs the shared terminal tail for every completion path.
func (e *Engine) settleTerminal(rec *taskRecord) {
	if rec.result.AttemptID == "" && rec.spec.Job != nil {
		rec.result.AttemptID = rec.spec.Job.AttemptID
	}
	if e.resultSlotsHeld > 0 {
		e.resultSlotsHeld--
	}
	if rec.spec.OnComplete != nil && rec.callbackReserved {
		_ = e.delivery.enqueueReserved(rec.spec.OnComplete, rec.result)
		rec.callbackReserved = false
	}
	// Terminal records retain only diagnostic identity and bounded results.
	// Execution closures may capture arbitrarily large object graphs and must
	// not remain reachable for the terminal retention window.
	rec.spec.Input = nil
	rec.spec.Job = nil
	rec.spec.Resources = nil
	rec.spec.OrderingKey = ""
	rec.spec.HandlerRef = ""
	rec.spec.QueueDeadline = time.Time{}
	rec.spec.ExecutionTimeout = 0
	rec.spec.Handler = nil
	rec.spec.Commit = nil
	rec.spec.OnComplete = nil
	rec.cancelFunc = nil
	rec.permit = nil
	rec.pendingResult = tasks.TaskResult{}
	close(rec.done)
	e.onTaskSettled(rec)
}

// applyWorkerCompleted accepts completion only from the physical grant that
// owns the current dispatch generation/epoch. This mirrors Started fencing and
// prevents a late completion from an evicted/reused TaskID mutating a new task.
func (e *Engine) applyWorkerCompleted(res tasks.TaskResult, grant *permit) {
	rec, ok := e.registry[res.TaskID]
	if !ok {
		return
	}
	if rec.state != tasks.StateDispatching && rec.state != tasks.StateRunning {
		return
	}
	if grant == nil || rec.permit != grant {
		return
	}
	if grant.generation != rec.poolGeneration || grant.dispatchEpoch != rec.dispatchEpoch {
		return
	}
	rec.result = res
	rec.finishedAt = res.FinishedAt
	if rec.cancelRequested {
		res.Outcome = tasks.OutcomeCancelled
		res.Cause = rec.cancelReason
		res.Disposition = execution.DispositionCancelled
		res.Failure.Code = string(rec.cancelReason)
		if res.Failure.Message == "" {
			res.Failure.Message = fmt.Sprintf("late cancellation applied: %s", rec.cancelReason)
		}
		rec.result = res
	}
	spec := rec.spec
	e.releaseResources(spec)
	e.adm.OnTaskTerminal(spec)
	if rec.cancelFunc != nil {
		rec.cancelFunc()
	}
	if rec.startedAt.IsZero() {
		rec.startedAt = res.StartedAt
	}
	for pool := range e.config.Pools {
		e.tryDispatch(pool)
	}
	bounded, delta := e.boundResult(rec.result)
	rec.result = bounded
	if rec.errorMsg != "" {
		rec.errorMsg = truncateField(rec.errorMsg, e.maxFailureBytes)
	}
	rec.retainedBytes += delta
	e.retainedBytes += delta

	if !spec.RequiresDurability() {
		rec.state = terminalStateFor(rec.result.Outcome)
		switch rec.state {
		case tasks.StateTimedOut, tasks.StateCancelled, tasks.StateFailed:
			rec.errorMsg = rec.result.Failure.Message
		}
		e.settleTerminal(rec)
		return
	}
	rec.execOutcome = rec.result.Outcome
	rec.pendingResult = rec.result
	e.beginCommit(rec)
}

// applyCancel is the single linearization point for cancellation vs dispatch.
func (e *Engine) applyCancel(id tasks.TaskID, reason tasks.Cause) (tasks.CancelReceipt, error) {
	rec, exists := e.registry[id]
	if !exists {
		return tasks.CancelReceipt{TaskID: id, Accepted: false, State: "", Reason: reason}, tasks.ErrTaskNotFound
	}
	switch rec.state {
	case tasks.StateCreated, tasks.StateAdmitted, tasks.StateQueued:
		e.adm.RemoveTask(id)
		rec.state = tasks.StateCancelled
		rec.cancelRequested = true
		rec.cancelReason = reason
		rec.finishedAt = time.Now().UTC()
		rec.result = tasks.TaskResult{TaskID: id, Outcome: tasks.OutcomeCancelled, Cause: reason, Disposition: execution.DispositionCancelled, FinishedAt: rec.finishedAt, Failure: tasks.FailureInfo{Code: string(reason)}}
		bounded, delta := e.boundResult(rec.result)
		rec.result = bounded
		rec.retainedBytes += delta
		e.retainedBytes += delta
		e.settleTerminal(rec)
		return tasks.CancelReceipt{TaskID: id, Accepted: true, State: tasks.StateCancelled, Reason: reason}, nil
	case tasks.StateDispatching, tasks.StateRunning:
		rec.cancelRequested = true
		rec.cancelReason = reason
		if rec.cancelFunc != nil {
			rec.cancelFunc()
		}
		return tasks.CancelReceipt{TaskID: id, Accepted: true, State: rec.state, Reason: reason}, nil
	case tasks.StateCommitPending:
		return tasks.CancelReceipt{TaskID: id, Accepted: false, State: rec.state, Reason: reason}, nil
	default:
		return tasks.CancelReceipt{TaskID: id, Accepted: false, State: rec.state, Reason: reason}, nil
	}
}

func (e *Engine) applyCancelScope(scope tasks.ScopeIdentity, reason tasks.Cause) int {
	if e.cancelledScopes == nil {
		e.cancelledScopes = make(map[tasks.ScopeIdentity]tasks.Cause)
	}
	if _, exists := e.cancelledScopes[scope]; !exists {
		e.cancelledScopeOrder = append(e.cancelledScopeOrder, scope)
	}
	e.cancelledScopes[scope] = reason
	for e.maxScopeTombstones > 0 && len(e.cancelledScopeOrder) > e.maxScopeTombstones {
		oldest := e.cancelledScopeOrder[0]
		e.cancelledScopeOrder[0] = tasks.ScopeIdentity{}
		e.cancelledScopeOrder = e.cancelledScopeOrder[1:]
		delete(e.cancelledScopes, oldest)
	}
	cancelled := 0
	for id, rec := range e.registry {
		if rec.spec.Scope.Owner != scope.Owner {
			continue
		}
		if scope.Generation != 0 && rec.spec.Scope.Generation != scope.Generation {
			continue
		}
		if rec.isTerminal() || rec.state == tasks.StateCommitPending {
			continue
		}
		receipt, err := e.applyCancel(id, reason)
		if err == nil && receipt.Accepted {
			cancelled++
		}
	}
	return cancelled
}

func (e *Engine) applyResult(id tasks.TaskID) (tasks.TaskResult, bool) {
	rec, ok := e.registry[id]
	if !ok {
		return tasks.TaskResult{}, false
	}
	switch rec.state {
	case tasks.StateCompleted, tasks.StateFailed, tasks.StateCancelled, tasks.StateTimedOut, tasks.StateRecoveryRequired:
		return rec.result, true
	default:
		return tasks.TaskResult{}, false
	}
}

func (e *Engine) applySnapshot(id tasks.TaskID) (tasks.TaskSnapshot, bool) {
	rec, ok := e.registry[id]
	if !ok {
		return tasks.TaskSnapshot{}, false
	}
	return tasks.TaskSnapshot{
		ID: rec.spec.ID, Scope: rec.spec.Scope, QuotaOwner: rec.spec.QuotaOwner,
		Pool: rec.spec.Pool, Class: rec.spec.Class, State: rec.state,
		AdmittedAt: rec.admittedAt, QueuedAt: rec.queuedAt, StartedAt: rec.startedAt,
		FinishedAt: rec.finishedAt, Error: rec.errorMsg,
	}, true
}

func (e *Engine) applyQuiesce() {
	e.accepting = false
	e.quiesced = true
	e.checkDrained()
}

func (e *Engine) applyStopFinalize() {
	e.accepting = false
	e.quiesced = true
	for id, rec := range e.registry {
		switch rec.state {
		case tasks.StateQueued:
			_, _ = e.applyCancel(id, tasks.CauseShutdown)
		case tasks.StateDispatching, tasks.StateRunning:
			e.forceCancelInFlight(rec)
		case tasks.StateCommitPending:
			e.abandonPending(rec, "shutdown")
		}
	}
	e.checkDrained()
}

// forceCancelInFlight is used only after graceful drain has failed (or
// ForceStop was requested). It releases admission/permit ownership and
// fences any later worker completion by moving the record terminal first.
func (e *Engine) forceCancelInFlight(rec *taskRecord) {
	if rec == nil || (rec.state != tasks.StateDispatching && rec.state != tasks.StateRunning) {
		return
	}
	rec.cancelRequested = true
	rec.cancelReason = tasks.CauseShutdown
	if rec.cancelFunc != nil {
		rec.cancelFunc()
	}
	if rec.permit != nil {
		rec.permit.release()
	}
	e.adm.OnTaskTerminal(rec.spec)

	now := time.Now().UTC()
	rec.state = tasks.StateCancelled
	rec.finishedAt = now
	failureMessage := "task cancelled by forced shutdown"
	if rec.spec.RequiresDurability() {
		// The physical handler may have crossed its external side-effect boundary
		// before the hard shutdown deadline. Without a durable acknowledgement we
		// must preserve that uncertainty instead of claiming cancellation was
		// committed. The persisted attempt/lease remains the recovery authority.
		rec.state = tasks.StateRecoveryRequired
		failureMessage = "durable in-flight task abandoned by forced shutdown; effect unknown; recovery required"
	}
	res := tasks.TaskResult{
		TaskID: rec.spec.ID, Outcome: tasks.OutcomeCancelled, Cause: tasks.CauseShutdown,
		Disposition: execution.DispositionCancelled,
		StartedAt:   rec.startedAt, FinishedAt: now,
		Failure: tasks.FailureInfo{Code: "shutdown", Message: failureMessage},
	}
	if rec.spec.Job != nil {
		res.AttemptID = rec.spec.Job.AttemptID
	}
	bounded, delta := e.boundResult(res)
	rec.result = bounded
	rec.errorMsg = bounded.Failure.Message
	rec.retainedBytes += delta
	e.retainedBytes += delta
	e.settleTerminal(rec)
}

func (e *Engine) onTaskSettled(rec *taskRecord) {
	e.syncActiveTasks(-1)
	if rec != nil && rec.isTerminal() {
		e.terminalOrder = append(e.terminalOrder, rec.spec.ID)
		e.evictTerminalRecords()
	}
	e.checkDrained()
}

func (e *Engine) evictTerminalRecords() {
	e.evictTerminalHead(func() bool {
		if e.maxTerminalRetained > 0 && len(e.terminalOrder) > e.maxTerminalRetained {
			return true
		}
		return e.maxRetainedBytes > 0 && e.retainedBytes > e.maxRetainedBytes
	})
}

func (e *Engine) evictExpiredTerminal(now time.Time) {
	if e.terminalTTL <= 0 {
		return
	}
	e.evictTerminalHead(func() bool {
		if len(e.terminalOrder) == 0 {
			return false
		}
		rec, ok := e.registry[e.terminalOrder[0]]
		if !ok || !rec.isTerminal() {
			return true
		}
		return now.Sub(rec.finishedAt) > e.terminalTTL
	})
}

func (e *Engine) earliestTerminalExpiry() (time.Time, bool) {
	if e.terminalTTL <= 0 {
		return time.Time{}, false
	}
	for _, id := range e.terminalOrder {
		rec, ok := e.registry[id]
		if !ok || !rec.isTerminal() || rec.finishedAt.IsZero() {
			continue
		}
		return rec.finishedAt.Add(e.terminalTTL), true
	}
	return time.Time{}, false
}

func (e *Engine) evictTerminalHead(shouldEvict func() bool) {
	for shouldEvict() {
		if len(e.terminalOrder) == 0 {
			return
		}
		oldestID := e.terminalOrder[0]
		e.terminalOrder[0] = ""
		e.terminalOrder = e.terminalOrder[1:]
		if oldRec, ok := e.registry[oldestID]; ok && oldRec.isTerminal() {
			e.retainedBytes -= oldRec.retainedBytes
			if e.retainedBytes < 0 {
				e.retainedBytes = 0
			}
			delete(e.registry, oldestID)
		}
	}
}

func (e *Engine) checkDrained() {
	if e.quiesced && e.activeTasks == 0 && !e.drained {
		e.drained = true
		select {
		case <-e.drainDone:
		default:
			close(e.drainDone)
		}
	}
}
