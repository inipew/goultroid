package taskengine

import (
	"context"
	"errors"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func (e *Engine) Name() string           { return "taskengine" }
func (e *Engine) Dependencies() []string { return nil }

// Start initializes the engine lifecycle and starts the single-writer control
// loop plus fixed physical worker loops.
func (e *Engine) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.configErr != nil {
		return e.configErr
	}
	if e.runStarted {
		return errors.New("task engine already started")
	}
	e.rootCtx, e.rootCancel = context.WithCancel(ctx)
	e.accepting = true
	e.quiesced = false
	e.drained = false
	e.drainDone = make(chan struct{})
	e.inbox = make(chan *engineRequest, e.inboxCap)
	inbox := e.inbox
	rootCtx := e.rootCtx
	e.runStarted = true
	if e.delivery == nil {
		e.delivery = newCompletionDelivery(DefaultDeliveryConcurrency, 256)
	}
	if e.durability == nil {
		e.durability = newDurabilityLane(defaultDurabilityConcurrency, e.resultCapacity)
	}
	delivery := e.delivery
	durability := e.durability
	delivery.start()
	durability.start()

	e.runtimeRemaining.Store(1)
	for poolID := range e.workerMailboxes {
		for slotID := 0; slotID < e.poolMinWorkers[poolID]; slotID++ {
			if e.spawnWorker(poolID, slotID, rootCtx) {
				e.idleSlots[poolID] = append(e.idleSlots[poolID], slotID)
			}
		}
	}
	go func() {
		defer e.runtimeLoopDone()
		e.runLoop(rootCtx, inbox)
	}()
	return nil
}

func (e *Engine) spawnWorker(pool tasks.PoolID, slot int, ctx context.Context) bool {
	running := e.workerRunning[pool]
	if slot < 0 || slot >= len(running) || running[slot] {
		return false
	}
	running[slot] = true
	workerCtx, cancel := context.WithCancel(ctx)
	e.workerCancels[pool][slot] = cancel
	e.workerIdleSince[pool][slot] = time.Now().UTC()
	e.runtimeRemaining.Add(1)

	// Each physical worker generation owns a distinct mailbox. Retirement only
	// cancels the old worker; it does not synchronously join it. Reusing the same
	// channel would let a cancelled generation race a newly spawned generation
	// for the next assignment. Replacing the mailbox before publishing the slot
	// idle makes stale workers incapable of consuming future work.
	mailbox := make(chan workerAssignment, 1)
	e.workerMailboxes[pool][slot] = mailbox
	go func() {
		defer e.runtimeLoopDone()
		e.physicalWorker(pool, slot, mailbox, workerCtx)
	}()
	return true
}

func (e *Engine) runtimeLoopDone() {
	if e.runtimeRemaining.Add(-1) == 0 {
		e.runtimeDoneOnce.Do(func() { close(e.runtimeDone) })
	}
}

func (e *Engine) releaseQueuedRequests(inbox <-chan *engineRequest) {
	for {
		select {
		case req, ok := <-inbox:
			if !ok {
				return
			}
			e.releaseRequest(req)
		default:
			return
		}
	}
}

func (e *Engine) finishCoordinator(inbox <-chan *engineRequest) {
	e.controlGate.Lock()
	defer e.controlGate.Unlock()

	e.mu.Lock()
	if e.inbox == inbox {
		e.inbox = nil
	}
	e.mu.Unlock()
	e.releaseQueuedRequests(inbox)
}

func (e *Engine) enqueueRequest(ctx context.Context, queued *engineRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.controlGate.RLock()
	defer e.controlGate.RUnlock()

	e.mu.Lock()
	inbox := e.inbox
	rootCtx := e.rootCtx
	e.mu.Unlock()
	if inbox == nil || rootCtx == nil || rootCtx.Err() != nil {
		return tasks.NewAdmissionError(tasks.ReasonEngineQuiescing, tasks.ErrEngineQuiescing)
	}
	select {
	case inbox <- queued:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-rootCtx.Done():
		return tasks.NewAdmissionError(tasks.ReasonEngineQuiescing, tasks.ErrEngineQuiescing)
	}
}

// runLoop is the sole writer of execution state.
func (e *Engine) runLoop(ctx context.Context, inbox <-chan *engineRequest) {
	// Once the coordinator exits no queued control request can be processed.
	// Clear pooled envelopes eagerly so their contexts/specs/results do not stay
	// reachable through the Engine's retained channel after shutdown.
	defer e.finishCoordinator(inbox)
	sweepTimer := time.NewTimer(time.Hour)
	if !sweepTimer.Stop() {
		select {
		case <-sweepTimer.C:
		default:
		}
	}
	defer sweepTimer.Stop()
	var sweepTimerCh <-chan time.Time

	armSweeper := func() {
		now := time.Now().UTC()
		var earliest time.Time
		hasEarliest := false

		if admEarliest, hasAdm := e.adm.EarliestDeadline(); hasAdm {
			earliest = admEarliest
			hasEarliest = true
		}
		if retEarliest, hasRet := e.earliestRetirementDeadline(now); hasRet {
			if !hasEarliest || retEarliest.Before(earliest) {
				earliest = retEarliest
				hasEarliest = true
			}
		}
		if ttlEarliest, hasTTL := e.earliestTerminalExpiry(); hasTTL {
			if !hasEarliest || ttlEarliest.Before(earliest) {
				earliest = ttlEarliest
				hasEarliest = true
			}
		}

		if !hasEarliest {
			sweepTimerCh = nil
			return
		}
		delay := time.Until(earliest)
		if delay < 0 {
			delay = 0
		}
		if !sweepTimer.Stop() {
			select {
			case <-sweepTimer.C:
			default:
			}
		}
		sweepTimer.Reset(delay)
		sweepTimerCh = sweepTimer.C
	}
	armSweeper()

	for {
		select {
		case <-ctx.Done():
			e.applyStopFinalize()
			return
		case reqPtr, ok := <-inbox:
			if !ok {
				return
			}
			if reqPtr == nil {
				continue
			}
			// Copy the small live request view onto the coordinator stack and
			// release the pooled envelope before running any potentially heavier
			// state transition. Referenced payloads remain owned by the copy.
			req := *reqPtr
			e.releaseRequest(reqPtr)
			e.handleRequest(ctx, req)
			armSweeper()
		case <-sweepTimerCh:
			now := time.Now().UTC()
			for poolID := range e.config.Pools {
				e.sweepExpired(poolID, now)
			}
			e.sweepIdleWorkers(now)
			armSweeper()
		}
	}
}

func (e *Engine) handleRequest(ctx context.Context, req engineRequest) {
	switch req.op {
	case opSubmit:
		ticket, err := e.admitSubmit(ctx, req.ctx, req.spec, req.decision)
		if err != nil && req.decision != nil {
			req.decision.decide(decisionRejected)
		}
		req.reply <- engineReply{ticket: ticket, err: err}
	case opCancel:
		receipt, err := e.applyCancel(req.taskID, req.reason)
		req.reply <- engineReply{receipt: receipt, err: err}
	case opCancelScope:
		n := e.applyCancelScope(req.scope, req.reason)
		req.reply <- engineReply{count: n}
	case opSnapshot:
		snap, found := e.applySnapshot(req.taskID)
		req.reply <- engineReply{snapshot: snap, found: found}
	case opResult:
		res, found := e.applyResult(req.taskID)
		req.reply <- engineReply{result: res, hasResult: found, found: found}
	case opStats:
		pools := e.snapshotPoolRuntimeStats()
		resources := make(map[string]ResourceRuntimeStats, len(e.resourceCapacity))
		for name, capacity := range e.resourceCapacity {
			resources[name] = ResourceRuntimeStats{Used: e.resourceUsed[name], Capacity: capacity}
		}
		req.reply <- engineReply{stats: engineStats{
			resultSlotsHeld: e.resultSlotsHeld,
			resultCapacity:  e.resultCapacity,
			activeTasks:     e.activeTasks,
			accepting:       e.lifecycleAccepting(),
			quiesced:        e.lifecycleQuiesced(),
			retainedBytes:   e.retainedBytes,
			retainedCap:     e.maxRetainedBytes,
			terminalCount:   len(e.terminalOrder),
			commitPending:   e.commitPending,
			deliveryQueued:  e.delivery.queueLen(),
			deliveryCap:     e.delivery.queueCap(),
			deliveryFailed:  e.delivery.fallbackCount(),
			durabilityQueue: e.durability.queueLen(),
			durabilityCap:   e.durability.queueCap(),
			durabilityFail:  e.durability.failureCount(),
			scopeTombstones: len(e.cancelledScopes),
			pools:           pools, resources: resources,
		}}
	case opWorkerStarted:
		e.applyWorkerStarted(req.taskID, req.permit, req.started)
	case opWorkerCompleted:
		e.applyWorkerCompleted(req.result, req.permit)
		e.markWorkerIdle(req.pool, req.slotID)
	case opCommitAck:
		e.applyCommitAck(req.taskID, req.commitSeq, req.ackErr)
	case opSweep:
		now := time.Now().UTC()
		for poolID := range e.config.Pools {
			e.sweepExpired(poolID, now)
		}
	case opQuiesce:
		e.applyQuiesce()
		if req.reply != nil {
			req.reply <- engineReply{}
		}
	case opSetOwnerLimits:
		if req.controlDecision != nil && !req.controlDecision.decide(controlDecisionApplied) {
			if req.reply != nil {
				req.reply <- engineReply{err: context.Canceled}
			}
			break
		}
		e.adm.SetOwnerLimits(req.owner, req.limits)
		if req.reply != nil {
			req.reply <- engineReply{}
		}
	case opConfigurePool:
		err := e.applyPoolConfig(req.pool, req.poolConfig)
		if req.reply != nil {
			req.reply <- engineReply{err: err}
		}
	case opSetResourceCapacity:
		err := e.applyResourceCapacity(req.resourceName, req.resourceCapacity)
		if req.reply != nil {
			req.reply <- engineReply{err: err}
		}
	case opStopFinalize:
		e.applyStopFinalize()
		if req.reply != nil {
			req.reply <- engineReply{}
		}
	}
}

func (e *Engine) lifecycleAccepting() bool { return e.accepting }
func (e *Engine) lifecycleQuiesced() bool  { return e.quiesced }

func (e *Engine) physicalWorker(pool tasks.PoolID, slotID int, mailbox <-chan workerAssignment, ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case assignment, ok := <-mailbox:
			if !ok {
				return
			}
			res := executeAssignment(assignment.taskCtx, assignment.spec, assignment.permit, func(startedAt time.Time) {
				e.sendInternal(engineRequest{op: opWorkerStarted, taskID: assignment.spec.ID, permit: assignment.permit, started: startedAt})
			})
			// Completion and return-to-idle are one control-loop transition so
			// diagnostics never expose a live worker in an unclassified gap.
			e.sendInternal(engineRequest{
				op: opWorkerCompleted, result: res, permit: assignment.permit,
				pool: pool, slotID: slotID,
			})
		}
	}
}

func (e *Engine) acquireRequest(req engineRequest) *engineRequest {
	pooled := e.requestPool.Get()
	var slot *engineRequest
	if pooled == nil {
		slot = &engineRequest{}
	} else {
		slot = pooled.(*engineRequest)
	}
	*slot = req
	return slot
}

func (e *Engine) releaseRequest(req *engineRequest) {
	if req == nil {
		return
	}
	// Do not let the pool extend lifetimes of contexts, closures, result
	// payloads, reply channels, or strings between control operations.
	*req = engineRequest{}
	e.requestPool.Put(req)
}

// sendInternal delivers worker-originated events to the control loop.
func (e *Engine) sendInternal(req engineRequest) {
	queued := e.acquireRequest(req)
	if err := e.enqueueRequest(context.Background(), queued); err != nil {
		e.releaseRequest(queued)
	}
}

// sendControl handles non-submit producer requests. Submit has a stronger
// decision-cell protocol below because cancellation after inbox handoff must
// not hide an admission decision.
func (e *Engine) sendControl(ctx context.Context, req engineRequest) (engineReply, error) {
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
	req.ctx = ctx
	req.reply = make(chan engineReply, 1)
	queued := e.acquireRequest(req)
	if err := e.enqueueRequest(ctx, queued); err != nil {
		e.releaseRequest(queued)
		return engineReply{}, err
	}
	select {
	case rep := <-req.reply:
		return rep, nil
	case <-ctx.Done():
		return engineReply{}, ctx.Err()
	case <-rootCtx.Done():
		return engineReply{}, tasks.NewAdmissionError(tasks.ReasonEngineQuiescing, tasks.ErrEngineQuiescing)
	}
}

// sendSubmitControl makes the inbox handoff explicit. Before handoff, caller
// cancellation simply prevents submission. After handoff, caller cancellation
// may win only by atomically fencing the still-pending coordinator decision.
// If the coordinator has already published accepted/rejected, Submit waits for
// that reply even if the caller context expires, eliminating ambiguous
// "returned error but task was accepted" outcomes.

// Quiesce stops accepting new tasks while letting already admitted tasks run.
func (e *Engine) Quiesce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := e.sendControl(ctx, engineRequest{op: opQuiesce})
	if errors.Is(err, tasks.ErrEngineQuiescing) {
		return nil
	}
	return err
}

// Drain waits until all admitted tasks and completion callbacks settle.
func (e *Engine) Drain(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	_ = e.Quiesce(ctx)
	e.mu.Lock()
	rootCtx := e.rootCtx
	drainDone := e.drainDone
	delivery := e.delivery
	e.mu.Unlock()
	if rootCtx == nil || drainDone == nil {
		return nil
	}
	select {
	case <-drainDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	if delivery == nil {
		return nil
	}
	return delivery.drain(ctx)
}

// Stop quiesces and drains normally, then performs bounded forced cleanup.
// A caller deadline is never replaced by fixed background waits.
func (e *Engine) Stop(ctx context.Context) error {
	return e.stopEngine(ctx, true)
}
