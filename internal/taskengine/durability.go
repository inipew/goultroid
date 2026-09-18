package taskengine

import (
	"context"
	"errors"
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
//   - Persistence waits/direct fallbacks use a dedicated bounded durability
//     lane, never the user completion-callback delivery workers.
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

// SizedCommitPump is an optional stronger admission contract. Pumps that
// implement it can reject retained-memory pressure before accepting a closure.
type SizedCommitPump interface {
	EnqueueSized(ctx context.Context, retainedBytes int64, op func(ctx context.Context) error) (<-chan error, error)
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

// directCommitTimeout bounds the fallback path that runs the commit in the
// dedicated durability lane when the pump is missing or saturated.
const (
	directCommitTimeout       = 15 * time.Second
	durabilityCommitBaseBytes = 512
)

func durabilityCommitRetainedBytes(res tasks.TaskResult) int64 {
	bytes := int64(durabilityCommitBaseBytes +
		len(res.TaskID) + len(res.AttemptID) + len(res.Outcome) + len(res.Cause) +
		len(res.Failure.Message) + len(res.Failure.Detail))
	switch output := res.Output.(type) {
	case []byte:
		bytes += int64(len(output))
	case string:
		bytes += int64(len(output))
	}
	return bytes
}

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
// its acknowledgement wait/direct commit to the bounded durability lane. The
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
	// Snapshot the callback and result. The task record can drop execution
	// closures as soon as it settles without extending their lifetime through
	// the durability transport.
	commit := rec.spec.Commit
	pendingResult := rec.pendingResult
	commitOp := func(ctx context.Context) error {
		return commit(ctx, pendingResult)
	}
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
		var (
			resCh <-chan error
			err   error
		)
		if sized, ok := e.commitPump.(SizedCommitPump); ok {
			resCh, err = sized.EnqueueSized(context.Background(), durabilityCommitRetainedBytes(pendingResult), commitOp)
		} else {
			resCh, err = e.commitPump.Enqueue(context.Background(), commitOp)
		}
		if err == nil {
			if e.durability != nil && e.durability.enqueue(func() { waitPump(resCh) }) {
				return
			}
			// The pump request may already be executing. Its result channel is
			// buffered, so failing the local acknowledgement lane cannot wedge the
			// pump. Resolve as uncertain and let durable recovery reconcile it.
			e.applyCommitAck(rec.spec.ID, rec.commitSeq, errors.New("durability acknowledgement lane saturated"))
			return
		}
	}

	// Pump missing or saturated: attempt the commit in the separate bounded
	// durability lane. No completion-callback worker and no detached goroutine
	// is consumed by this path.
	if e.durability != nil && e.durability.enqueue(func() {
		commitCtx, cancel := context.WithTimeout(context.Background(), directCommitTimeout)
		defer cancel()
		ackErr := commitOp(commitCtx)
		select {
		case <-rootCtx.Done():
			return
		default:
		}
		e.sendInternal(engineRequest{op: opCommitAck, taskID: rec.spec.ID, commitSeq: rec.commitSeq, ackErr: ackErr})
	}) {
		return
	}

	// Fail closed when even the bounded durability lane is saturated. This is
	// explicit RecoveryRequired state, never an unbounded rescue goroutine.
	e.applyCommitAck(rec.spec.ID, rec.commitSeq, errors.New("durability commit lane saturated"))
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
	// the state transition below, and this unblocks durability shutdown.
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
