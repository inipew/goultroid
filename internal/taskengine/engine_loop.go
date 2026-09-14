package taskengine

import (
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/tasks"
)

func (e *Engine) loop() {
	defer func() {
		e.mu.Lock()
		e.running = false
		close(e.done)
		e.mu.Unlock()
	}()

	rootDone := e.ctx.Done()
	timer := e.clock.NewTimer(time.Hour)
	if !timer.Stop() {
		select {
		case <-timer.C():
		default:
		}
	}
	defer timer.Stop()

	for {
		e.dispatchAvailable()
		e.notifyDrainedIfSettled()

		wait, hasDeadline := e.nextTimerWait(e.clock.Now().UTC())
		var timerC <-chan time.Time
		if hasDeadline {
			if wait < 0 {
				wait = 0
			}
			timer.Reset(wait)
			timerC = timer.C()
		}

		select {
		case message := <-e.control:
			e.stopTimer(timer, hasDeadline)
			e.handleControl(message)
		case request := <-e.admissions:
			e.stopTimer(timer, hasDeadline)
			e.handleSubmit(request)
		case event := <-e.startedEvents:
			e.stopTimer(timer, hasDeadline)
			event.response <- e.handleStarted(event)
		case event := <-e.results:
			e.stopTimer(timer, hasDeadline)
			_ = e.handleCompleted(event)
		case now := <-timerC:
			e.expireDue(now.UTC(), 64)
		case <-rootDone:
			e.stopTimer(timer, hasDeadline)
			e.accepting = false
			e.cancelAcceptedForShutdown()
			rootDone = nil
		}

		if rootDone == nil && e.unsettledCount() == 0 {
			e.releaseAllRetainedCredits()
			return
		}
	}
}

func (e *Engine) stopTimer(timer Timer, armed bool) {
	if !armed {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C():
		default:
		}
	}
}

func (e *Engine) handleControl(message any) {
	switch request := message.(type) {
	case cancelRequest:
		receipt, err := e.cancelTask(request.taskID, request.reason, e.clock.Now().UTC())
		request.response <- cancelResponse{receipt: receipt, err: err}
	case snapshotRequest:
		snapshot, ok := e.snapshot(request.taskID)
		request.response <- snapshotResponse{snapshot: snapshot, ok: ok}
	case consumeRequest:
		result, ok, err := e.consumeResult(request.taskID)
		request.response <- consumeResponse{result: result, ok: ok, err: err}
	case statsRequest:
		request.response <- e.stats()
	case quiesceRequest:
		e.accepting = false
		request.response <- struct{}{}
	case drainRequest:
		waiter := make(chan struct{})
		if e.unsettledCount() == 0 {
			close(waiter)
		} else {
			e.drainWaiters = append(e.drainWaiters, waiter)
		}
		request.response <- waiter
	case closeScopeRequest:
		owner := request.scope.Owner()
		if request.scope.Generation() > e.closedScopes[owner] {
			e.closedScopes[owner] = request.scope.Generation()
		}
		count := 0
		now := e.clock.Now().UTC()
		for id, record := range e.records {
			if record.spec.Scope() != request.scope || record.state.Terminal() {
				continue
			}
			if _, err := e.cancelTask(id, tasks.CancelScopeClosed, now); err == nil {
				count++
			}
		}
		request.response <- count
	}
}

func (e *Engine) handleSubmit(request submitRequest) {
	now := e.clock.Now().UTC()
	respondReject := func(reason tasks.RejectionReason, cause error) {
		err := error(&tasks.AdmissionError{Reason: reason})
		if cause != nil {
			err = fmt.Errorf("%w: %v", err, cause)
		}
		request.response <- submitResponse{err: err}
	}

	if request.ctx.Err() != nil {
		request.response <- submitResponse{err: request.ctx.Err()}
		return
	}
	if now.Sub(request.requestedAt) > e.cfg.AdmissionDecisionLimit {
		respondReject(tasks.RejectDeadlineExpired, errors.New("admission decision deadline exceeded"))
		return
	}
	if !e.accepting {
		respondReject(tasks.RejectEngineQuiescing, nil)
		return
	}
	if request.spec.ID() == "" {
		respondReject(tasks.RejectInvalidSpec, errors.New("task ID is required"))
		return
	}
	if _, exists := e.records[request.spec.ID()]; exists {
		respondReject(tasks.RejectInvalidSpec, errors.New("task ID already exists"))
		return
	}
	if err := e.catalog.ValidateSpec(request.spec); err != nil {
		respondReject(tasks.RejectInvalidSpec, err)
		return
	}
	if _, durable := request.spec.Job(); durable {
		respondReject(tasks.RejectInvalidSpec, errors.New("durable prepare protocol is not configured in P2 executor"))
		return
	}
	if closedGeneration, closed := e.closedScopes[request.spec.Scope().Owner()]; closed && request.spec.Scope().Generation() <= closedGeneration {
		respondReject(tasks.RejectScopeClosed, nil)
		return
	}

	deadline := request.spec.QueueDeadline()
	if deadline.IsZero() {
		deadline = now.Add(e.cfg.QueueTimeout)
	}
	if !deadline.After(now) {
		respondReject(tasks.RejectDeadlineExpired, nil)
		return
	}
	if e.resultCreditsUsed >= e.cfg.ResultCredits {
		respondReject(tasks.RejectResultBackpress, nil)
		return
	}

	poolLimits := e.cfg.Pools[request.spec.Pool()]
	pool := e.pools[request.spec.Pool()]
	owner := e.owners[request.spec.QuotaOwner()]
	if owner == nil {
		owner = &ownerUsage{}
	}
	payloadBytes := int64(request.spec.Input().Size())
	if pool.waiting >= poolLimits.MaxWaiting {
		respondReject(tasks.RejectPoolBacklogFull, nil)
		return
	}
	ownerLimits := e.ownerLimits(request.spec.QuotaOwner())
	if owner.waiting >= ownerLimits.MaxWaiting {
		respondReject(tasks.RejectOwnerQueueFull, nil)
		return
	}
	if pool.waitingBytes+payloadBytes > poolLimits.MaxWaitingBytes || owner.waitingBytes+payloadBytes > ownerLimits.MaxWaitingBytes {
		respondReject(tasks.RejectPayloadBudget, nil)
		return
	}

	ticket, err := tasks.NewAdmissionTicket(request.spec.ID(), now)
	if err != nil {
		respondReject(tasks.RejectInvalidSpec, err)
		return
	}
	record := &taskRecord{
		spec: request.spec, ticket: ticket, state: tasks.LifecycleQueued,
		createdAt: request.requestedAt, admittedAt: now, queuedAt: now,
		queueDeadline: deadline, creditHeld: true,
	}
	e.records[request.spec.ID()] = record
	e.resultCreditsUsed++
	pool.waiting++
	pool.waitingBytes += payloadBytes
	if e.owners[request.spec.QuotaOwner()] == nil {
		e.owners[request.spec.QuotaOwner()] = owner
	}
	owner.waiting++
	owner.waitingBytes += payloadBytes

	item := admission.Item{TaskID: request.spec.ID(), Pool: request.spec.Pool(), Class: request.spec.Class(), Owner: request.spec.QuotaOwner(), PayloadBytes: payloadBytes}
	if err := e.ready.Enqueue(item); err != nil {
		e.rollbackAdmission(record)
		respondReject(tasks.RejectInvalidSpec, err)
		return
	}
	if err := e.deadlines.Upsert(request.spec.ID(), deadline); err != nil {
		_, _ = e.ready.Remove(request.spec.ID())
		e.rollbackAdmission(record)
		respondReject(tasks.RejectInvalidSpec, err)
		return
	}
	request.response <- submitResponse{ticket: ticket}
}

func (e *Engine) rollbackAdmission(record *taskRecord) {
	delete(e.records, record.spec.ID())
	if record.creditHeld && e.resultCreditsUsed > 0 {
		e.resultCreditsUsed--
	}
	payloadBytes := int64(record.spec.Input().Size())
	pool := e.pools[record.spec.Pool()]
	pool.waiting--
	pool.waitingBytes -= payloadBytes
	owner := e.owner(record.spec.QuotaOwner())
	owner.waiting--
	owner.waitingBytes -= payloadBytes
	e.deleteOwnerIfIdle(record.spec.QuotaOwner())
}
