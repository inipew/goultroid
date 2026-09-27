package taskengine

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/tasks"
)

func (e *Engine) sendSubmitControl(ctx context.Context, spec tasks.WorkSpec) (engineReply, error) {
	e.mu.Lock()
	rootCtx := e.rootCtx
	timeout := e.decisionTimeout
	e.mu.Unlock()
	if rootCtx == nil {
		return engineReply{}, tasks.NewAdmissionError(tasks.ReasonEngineQuiescing, tasks.ErrEngineQuiescing)
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	decision := &submitCell{}
	reply := make(chan engineReply, 1)
	req := engineRequest{op: opSubmit, ctx: ctx, spec: spec, decision: decision, reply: reply}
	queued := e.acquireRequest(req)
	if err := e.enqueueRequest(ctx, queued); err != nil {
		e.releaseRequest(queued)
		return engineReply{}, err
	}

	ctxDone := ctx.Done()
	rootDone := rootCtx.Done()
	for {
		select {
		case rep := <-reply:
			return rep, nil
		case <-ctxDone:
			if decision.decide(decisionCancelled) {
				return engineReply{}, tasks.NewAdmissionError(tasks.ReasonLinearizationCancel, errors.Join(tasks.ErrLinearizationCancel, ctx.Err()))
			}
			// Coordinator already published accepted/rejected; cancellation can
			// no longer mask it. Disable this closed channel and wait for reply.
			ctxDone = nil
		case <-rootDone:
			if decision.decide(decisionCancelled) {
				return engineReply{}, tasks.NewAdmissionError(tasks.ReasonEngineQuiescing, tasks.ErrEngineQuiescing)
			}
			rootDone = nil
		}
	}
}

func (e *Engine) admitSubmit(loopCtx context.Context, callerCtx context.Context, spec tasks.WorkSpec, decision *submitCell) (tasks.Ticket, error) {
	if callerCtx != nil {
		if err := callerCtx.Err(); err != nil {
			return nil, tasks.NewAdmissionError(tasks.ReasonLinearizationCancel, errors.Join(tasks.ErrLinearizationCancel, err))
		}
	}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("invalid work spec: %w", err)
	}
	if _, exists := e.registry[spec.ID]; exists {
		return nil, errors.New("task id already registered")
	}
	if spec.Handler == nil && spec.HandlerRef != "" && e.handlerResolver != nil {
		handler, err := e.handlerResolver.ResolveHandler(spec.HandlerRef, spec.Input)
		if err != nil || handler == nil {
			return nil, tasks.NewAdmissionError(tasks.ReasonUnknownHandler, errors.Join(tasks.ErrUnknownHandler, err))
		}
		spec.Handler = handler
	}
	if spec.Handler == nil {
		return nil, tasks.NewAdmissionError(tasks.ReasonUnknownHandler, tasks.ErrUnknownHandler)
	}
	if !spec.QueueDeadline.IsZero() && !time.Now().Before(spec.QueueDeadline) {
		return nil, tasks.NewAdmissionError(tasks.ReasonDeadlineExpired, tasks.ErrDeadlineExpired)
	}
	if !e.accepting {
		return nil, tasks.NewAdmissionError(tasks.ReasonEngineQuiescing, tasks.ErrEngineQuiescing)
	}
	if spec.Scope.Owner != "" {
		if cause, closed := e.cancelledScopes[spec.Scope]; closed {
			return nil, tasks.NewAdmissionError(tasks.ReasonScopeClosed, fmt.Errorf("%w: scope %s (generation %d) is closed (%s)", tasks.ErrScopeClosed, spec.Scope.Owner, spec.Scope.Generation, cause))
		}
		if cause, closed := e.cancelledScopes[tasks.ScopeIdentity{Owner: spec.Scope.Owner, Generation: 0}]; closed {
			return nil, tasks.NewAdmissionError(tasks.ReasonScopeClosed, fmt.Errorf("%w: scope %s is closed (%s)", tasks.ErrScopeClosed, spec.Scope.Owner, cause))
		}
	}
	if e.resultCapacity > 0 && e.resultSlotsHeld >= e.resultCapacity {
		return nil, tasks.NewAdmissionError(tasks.ReasonResultBackpressure, tasks.ErrResultBackpressure)
	}

	payloadBytes, err := payloadSize(spec.Input)
	if err != nil {
		return nil, tasks.NewAdmissionError(tasks.ReasonUnsupportedPayload, err)
	}
	if spec.Job != nil {
		payloadBytes += jobRefBytes
	}
	for _, requirement := range spec.Resources {
		capacity, configured := e.resourceCapacity[requirement.Name]
		if !configured || requirement.Amount > capacity {
			return nil, tasks.NewAdmissionError(tasks.ReasonResourceUnavailable, tasks.ErrResourceUnavailable)
		}
	}
	if err := e.adm.CanAdmit(spec, payloadBytes); err != nil {
		return nil, err
	}
	retainedCharge := taskOverheadBytes + payloadBytes
	if e.maxRetainedBytes > 0 && e.retainedBytes+retainedCharge > e.maxRetainedBytes {
		return nil, tasks.NewAdmissionError(tasks.ReasonRetainedBudget, tasks.ErrRetainedBudget)
	}

	// Allocate the defensive copy only after all byte budgets accepted it.
	frozenInput, err := freezePayload(spec.Input)
	if err != nil {
		return nil, tasks.NewAdmissionError(tasks.ReasonUnsupportedPayload, err)
	}
	spec.Input = frozenInput
	if spec.Job != nil {
		ref := *spec.Job
		spec.Job = &ref
	}
	spec.Resources = append([]tasks.ResourceRequirement(nil), spec.Resources...)

	// Final caller-state check before any completion-delivery reservation or
	// published admission decision.
	if callerCtx != nil {
		if err := callerCtx.Err(); err != nil {
			return nil, tasks.NewAdmissionError(tasks.ReasonLinearizationCancel, errors.Join(tasks.ErrLinearizationCancel, err))
		}
	}

	callbackReserved := false
	if spec.OnComplete != nil {
		if e.delivery == nil || !e.delivery.reserve() {
			return nil, tasks.NewAdmissionError(tasks.ReasonDeliveryBackpressure, tasks.ErrDeliveryBackpressure)
		}
		callbackReserved = true
	}

	// This CAS is the admission linearization point. After it succeeds there
	// are no fallible operations before registry publication. If producer
	// cancellation won after inbox handoff, release any callback reservation
	// and reject without creating a record.
	if decision != nil && !decision.decide(decisionAccepted) {
		if callbackReserved {
			e.delivery.releaseReservation()
		}
		return nil, tasks.NewAdmissionError(tasks.ReasonLinearizationCancel, tasks.ErrLinearizationCancel)
	}

	now := time.Now().UTC()
	doneCh := make(chan struct{})
	e.commitSeq++
	rec := &taskRecord{
		spec:             spec,
		state:            tasks.StateAdmitted,
		done:             doneCh,
		retainedBytes:    retainedCharge,
		commitSeq:        e.commitSeq,
		callbackReserved: callbackReserved,
		admittedAt:       now,
		queuedAt:         now,
	}
	ticket := &engineTicket{taskID: spec.ID, engine: e, done: doneCh, rec: rec}
	rec.ticket = ticket
	e.registry[spec.ID] = rec
	e.resultSlotsHeld++
	e.retainedBytes += retainedCharge
	e.syncActiveTasks(1)
	rec.state = tasks.StateQueued
	e.adm.Enqueue(&admission.QueueEntry{Spec: spec, EnqueuedAt: now, PayloadSize: payloadBytes})
	e.tryDispatch(spec.Pool)
	return ticket, nil
}


// SetOwnerLimits sets quota and weight limits for an owner.
func (e *Engine) SetOwnerLimits(owner tasks.OwnerID, limits admission.OwnerLimits) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = e.sendControl(ctx, engineRequest{op: opSetOwnerLimits, owner: owner, limits: limits})
}

type submitDecisionState uint32

const (
	decisionPending submitDecisionState = iota
	decisionAccepted
	decisionRejected
	decisionCancelled
)

type submitCell struct {
	state atomic.Uint32
}

func (c *submitCell) decide(next submitDecisionState) bool {
	if c == nil {
		return false
	}
	return c.state.CompareAndSwap(uint32(decisionPending), uint32(next))
}

// Submit validates, reserves capacity, and enqueues work into the coordinator.
func (e *Engine) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rep, err := e.sendSubmitControl(ctx, spec)
	if err != nil {
		return nil, err
	}
	return rep.ticket, rep.err
}
