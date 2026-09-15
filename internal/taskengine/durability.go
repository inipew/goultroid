package taskengine

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

// Durable commit protocol (Phase C).
//
// The engine separates two lifetimes that the old runtime conflated:
//
//	ExecutionState:  Queued → Dispatching → Running → physically terminal
//	DurabilityState: None | CommitPending → Committed | RecoveryRequired
//
//   - Physical permit: released by the worker at handler return (executor).
//     Physical capacity is never held hostage by a slow durable store.
//   - Result credit: held from admission until durable resolution for tasks
//     that opt in via WorkSpec.Commit, released at physical completion
//     otherwise. A stalled store therefore surfaces as admission
//     backpressure (Phase B retained/result budgets), not silent loss.
//   - Retention: released only at eviction, as before.
//
// A task opts into durability with a non-nil Commit func. Tasks without one
// (interactive Telegram work, observers, periodic maintenance) resolve their
// ticket at physical completion exactly as before.
//
// The commit operation runs on the CommitPump (the jobs PersistencePump in
// production, a stub in tests). The pump reports back through CommitAck,
// fenced by task ID + commit sequence so late/duplicate acknowledgements from
// a previous generation can never resolve the wrong record. Ticket Done closes
// only at Committed or RecoveryRequired: a waiter observing success knows the
// evidence is durable, and RecoveryRequired tells Phase D recovery that the
// store is the source of truth.

// CommitPump is the durable-commit transport. It must never borrow TaskEngine
// physical slots (no cycles: tasks may wait on persistence to finish).
// *jobs.PersistencePump satisfies this structurally; the interface keeps the
// taskengine → jobs import forbidden by the architecture gate.
type CommitPump interface {
	Enqueue(ctx context.Context, op func(ctx context.Context) error) (<-chan error, error)
}

// durabilityState is the internal durability phase of one task record.
type durabilityState int

const (
	durNone durabilityState = iota
	durPending
	durCommitted
	durRecovery
)

// commitWaitTimeout bounds how long a commit waiter blocks on the pump result
// before reporting an uncertain acknowledgement.
const commitWaitTimeout = 30 * time.Second

// directCommitTimeout bounds the fallback path that runs the commit inline in
// the delivery pool when the pump is missing or saturated.
const directCommitTimeout = 15 * time.Second

// terminalStateFor maps a physical outcome to its public terminal state.
func terminalStateFor(outcome tasks.Outcome) tasks.TaskState {
	switch outcome {
	case tasks.OutcomeCompleted:
		return tasks.StateCompleted
	case tasks.OutcomeTimedOut:
		return tasks.StateTimedOut
	case tasks.OutcomeCancelled:
		return tasks.StateCancelled
	default:
		return tasks.StateFailed
	}
}

// beginCommit moves a physically complete record into CommitPending and hands
// its commit operation to the pump (or the bounded direct fallback). The
// caller must have stored the bounded physical result in rec.pendingResult,
// released quota/ordering, and dispatched further work already.
func (e *Engine) beginCommit(rec *taskRecord) {
	rec.state = tasks.StateCommitPending
	rec.durability = durPending
	e.commitPending++

	rootCtx := e.rootCtx
	waitCtx, waitCancel := context.WithCancel(context.Background())
	if e.commitWaiters == nil {
		e.commitWaiters = make(map[uint64]context.CancelFunc)
	}
	e.commitWaiters[rec.commitSeq] = waitCancel
	commitOp := func(ctx context.Context) error {
		return rec.spec.Commit(ctx, rec.pendingResult)
	}
	// waitPump blocks for the pump result, then forwards the acknowledgement.
	// Returning without an ack is only safe when the wait was abandoned
	// (shutdown: abandonPending already resolved the record) or the engine is
	// gone (a late ack would be fenced off anyway).
	waitPump := func(resCh <-chan error) {
		var ackErr error
		select {
		case ackErr = <-resCh:
		case <-waitCtx.Done():
			return
		case <-rootCtx.Done():
			return
		case <-time.After(commitWaitTimeout):
			ackErr = context.DeadlineExceeded
		}
		e.sendInternal(engineRequest{op: opCommitAck, taskID: rec.spec.ID, commitSeq: rec.commitSeq, ackErr: ackErr})
	}
	if e.commitPump != nil {
		if resCh, err := e.commitPump.Enqueue(context.Background(), commitOp); err == nil {
			e.delivery.enqueue(func(tasks.TaskResult) { waitPump(resCh) }, rec.pendingResult)
			return
		}
	}
	// Fallback: pump missing or saturated. Run the commit inline in the
	// bounded delivery pool so durable evidence is still attempted without
	// blocking the control loop or spawning unbounded goroutines.
	e.delivery.enqueue(func(tasks.TaskResult) {
		commitCtx, cancel := context.WithTimeout(context.Background(), directCommitTimeout)
		defer cancel()
		ackErr := commitOp(commitCtx)
		select {
		case <-rootCtx.Done():
			return
		default:
		}
		e.sendInternal(engineRequest{op: opCommitAck, taskID: rec.spec.ID, commitSeq: rec.commitSeq, ackErr: ackErr})
	}, rec.pendingResult)
}

// applyCommitAck resolves a CommitPending record. Success preserves the
// physical outcome as the public terminal state; failure or uncertainty moves
// to RecoveryRequired with the physical outcome preserved but the cause set to
// persistence failure, so Phase D recovery can reconcile against the store.
// Stale acks (unknown ID, wrong sequence, no longer pending) are ignored.
func (e *Engine) applyCommitAck(id tasks.TaskID, seq uint64, ackErr error) {
	rec, ok := e.registry[id]
	if !ok {
		return
	}
	if rec.state != tasks.StateCommitPending || rec.durability != durPending || rec.commitSeq != seq {
		return
	}
	e.releaseCommitWaiter(seq)
	e.commitPending--
	if ackErr == nil {
		rec.durability = durCommitted
		rec.state = terminalStateFor(rec.execOutcome)
		rec.result = rec.pendingResult
		switch rec.state {
		case tasks.StateTimedOut, tasks.StateCancelled, tasks.StateFailed:
			rec.errorMsg = rec.result.Failure.Message
		}
	} else {
		rec.durability = durRecovery
		rec.state = tasks.StateRecoveryRequired
		res := rec.pendingResult
		note := "durable commit failed or uncertain: " + ackErr.Error()
		if res.Failure.Message == "" {
			res.Failure.Message = note
		} else {
			res.Failure.Message += "; " + note
		}
		res.Cause = tasks.CausePersistenceFailure
		oldLen := len(rec.result.Failure.Message)
		res.Failure.Message = truncateField(res.Failure.Message, e.maxFailureBytes)
		res.Failure.Detail = truncateField(res.Failure.Detail, e.maxFailureBytes)
		rec.result = res
		rec.errorMsg = res.Failure.Message
		if delta := int64(len(res.Failure.Message) - oldLen); delta > 0 {
			rec.retainedBytes += delta
			e.retainedBytes += delta
		}
	}
	e.settleTerminal(rec)
}

// abandonPending marks a CommitPending record RecoveryRequired without
// acknowledgement (forced shutdown path). The commit operation may still be
// sitting in the pump; its late ack is fenced off by the state check above,
// and the durable store remains the source of truth.
func (e *Engine) abandonPending(rec *taskRecord, reason string) {
	if rec.state != tasks.StateCommitPending {
		return
	}
	// Release the commit waiter first: its late ack (if any) is fenced off by
	// the state transition below, and this unblocks delivery shutdown.
	e.releaseCommitWaiter(rec.commitSeq)
	e.commitPending--
	rec.durability = durRecovery
	rec.state = tasks.StateRecoveryRequired
	res := rec.pendingResult
	note := "engine shutdown before durable acknowledgement: " + reason
	if res.Failure.Message == "" {
		res.Failure.Message = note
	} else {
		res.Failure.Message += "; " + note
	}
	res.Cause = tasks.CausePersistenceFailure
	oldLen := len(rec.result.Failure.Message)
	res.Failure.Message = truncateField(res.Failure.Message, e.maxFailureBytes)
	rec.result = res
	rec.errorMsg = res.Failure.Message
	if delta := int64(len(res.Failure.Message) - oldLen); delta > 0 {
		rec.retainedBytes += delta
		e.retainedBytes += delta
	}
	e.settleTerminal(rec)
}

// releaseCommitWaiter cancels a pending commit wait (if any) and drops its
// registration. Called only from runLoop.
func (e *Engine) releaseCommitWaiter(seq uint64) {
	if cancel, ok := e.commitWaiters[seq]; ok {
		delete(e.commitWaiters, seq)
		cancel()
	}
}
