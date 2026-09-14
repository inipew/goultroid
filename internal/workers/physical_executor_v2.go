package workers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

var (
	ErrExecutorNotRunning = errors.New("physical executor is not running")
	ErrWorkerBusy         = errors.New("physical worker is busy")
	ErrInvalidPermit      = errors.New("physical permit does not match worker slot")
)

type assignmentEnvelope struct {
	ctx        context.Context
	assignment tasks.WorkerAssignment
	events     tasks.WorkerEvents
}

type physicalSlot struct {
	pool       tasks.PoolID
	id         tasks.WorkerID
	generation uint64
	mailbox    chan assignmentEnvelope

	mu   sync.Mutex
	busy bool
}

// PhysicalExecutor owns fixed physical worker slots. Each slot has exactly one
// assignment mailbox and cannot accept another assignment until its current
// attempt has returned and the result event has been emitted.
type PhysicalExecutor struct {
	mu       sync.RWMutex
	resolver tasks.HandlerResolver
	slots    []*physicalSlot
	byID     map[tasks.WorkerID]*physicalSlot
	running  bool
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	wg       sync.WaitGroup
}

// NewPhysicalExecutor constructs fixed worker inventory from per-pool counts.
func NewPhysicalExecutor(counts map[tasks.PoolID]int, resolver tasks.HandlerResolver) (*PhysicalExecutor, error) {
	if resolver == nil {
		return nil, errors.New("handler resolver is required")
	}
	executor := &PhysicalExecutor{resolver: resolver, byID: make(map[tasks.WorkerID]*physicalSlot)}
	for pool, count := range counts {
		if pool == "" || count <= 0 {
			return nil, errors.New("worker pool and positive count are required")
		}
		for index := 0; index < count; index++ {
			id := tasks.WorkerID(fmt.Sprintf("%s-%d", pool, index+1))
			if _, exists := executor.byID[id]; exists {
				return nil, fmt.Errorf("duplicate worker ID: %s", id)
			}
			slot := &physicalSlot{pool: pool, id: id, mailbox: make(chan assignmentEnvelope, 1)}
			executor.slots = append(executor.slots, slot)
			executor.byID[id] = slot
		}
	}
	if len(executor.slots) == 0 {
		return nil, errors.New("at least one worker slot is required")
	}
	return executor, nil
}

// Start activates worker goroutines and increments every slot generation. A
// permit from a previous Start can therefore never be reused after restart.
func (e *PhysicalExecutor) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return nil
	}
	if e.done != nil {
		select {
		case <-e.done:
		default:
			return errors.New("physical executor is still stopping")
		}
	}
	e.ctx, e.cancel = context.WithCancel(ctx)
	e.done = make(chan struct{})
	e.running = true
	for _, slot := range e.slots {
		slot.mu.Lock()
		slot.generation++
		generation := slot.generation
		slot.busy = false
		slot.mu.Unlock()
		e.wg.Add(1)
		go e.workerLoop(e.ctx, slot, generation)
	}
	done := e.done
	go func() {
		e.wg.Wait()
		close(done)
	}()
	return nil
}

// Stop cancels executor-owned waiting. Running handlers receive cancellation,
// but an uncooperative handler still owns its physical slot until it returns.
func (e *PhysicalExecutor) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	cancel := e.cancel
	done := e.done
	wasRunning := e.running
	e.running = false
	e.mu.Unlock()
	if wasRunning && cancel != nil {
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

func (e *PhysicalExecutor) Slots() []tasks.WorkerSlot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]tasks.WorkerSlot, 0, len(e.slots))
	for _, slot := range e.slots {
		slot.mu.Lock()
		result = append(result, tasks.WorkerSlot{Pool: slot.pool, WorkerID: slot.id, Generation: slot.generation})
		slot.mu.Unlock()
	}
	return result
}

func (e *PhysicalExecutor) Assign(ctx context.Context, assignment tasks.WorkerAssignment, events tasks.WorkerEvents) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if events == nil {
		return errors.New("worker events sink is required")
	}
	permit := assignment.Permit()
	e.mu.RLock()
	running := e.running
	slot := e.byID[permit.WorkerID()]
	executorCtx := e.ctx
	e.mu.RUnlock()
	if !running {
		return ErrExecutorNotRunning
	}
	if slot == nil || slot.pool != permit.Pool() {
		return ErrInvalidPermit
	}
	slot.mu.Lock()
	if slot.generation != permit.WorkerGeneration() {
		slot.mu.Unlock()
		return ErrInvalidPermit
	}
	if slot.busy {
		slot.mu.Unlock()
		return ErrWorkerBusy
	}
	slot.busy = true
	slot.mu.Unlock()

	envelope := assignmentEnvelope{ctx: ctx, assignment: assignment, events: events}
	select {
	case slot.mailbox <- envelope:
		return nil
	case <-ctx.Done():
		slot.mu.Lock()
		slot.busy = false
		slot.mu.Unlock()
		return ctx.Err()
	case <-executorCtx.Done():
		slot.mu.Lock()
		slot.busy = false
		slot.mu.Unlock()
		return ErrExecutorNotRunning
	default:
		// busy=true should make this impossible unless internal accounting has
		// drifted. Fail closed rather than create a hidden physical backlog.
		slot.mu.Lock()
		slot.busy = false
		slot.mu.Unlock()
		return ErrWorkerBusy
	}
}

func (e *PhysicalExecutor) workerLoop(executorCtx context.Context, slot *physicalSlot, generation uint64) {
	defer e.wg.Done()
	for {
		select {
		case envelope := <-slot.mailbox:
			if executorCtx.Err() != nil {
				e.abortBeforeStart(slot, generation, envelope)
				return
			}
			e.execute(executorCtx, slot, generation, envelope)
		case <-executorCtx.Done():
			select {
			case envelope := <-slot.mailbox:
				e.abortBeforeStart(slot, generation, envelope)
			default:
			}
			return
		}
	}
}

func (e *PhysicalExecutor) abortBeforeStart(slot *physicalSlot, generation uint64, envelope assignmentEnvelope) {
	permit := envelope.assignment.Permit()
	slot.mu.Lock()
	slot.busy = false
	slot.mu.Unlock()
	if permit.WorkerGeneration() != generation {
		return
	}
	result, err := tasks.NewTaskResult(tasks.TaskResultParams{
		TaskID: permit.TaskID(), Outcome: tasks.OutcomeCancelled, Cause: tasks.ResultCauseEngineShutdown,
		FinishedAt: time.Now().UTC(), Failure: tasks.FailureInfo{Code: "engine_shutdown", Message: "executor stopped before handler start"},
	})
	if err == nil {
		_ = envelope.events.Completed(permit, result)
	}
}

func (e *PhysicalExecutor) execute(executorCtx context.Context, slot *physicalSlot, generation uint64, envelope assignmentEnvelope) {
	released := false
	releaseSlot := func() {
		if released {
			return
		}
		slot.mu.Lock()
		slot.busy = false
		slot.mu.Unlock()
		released = true
	}
	defer releaseSlot()

	permit := envelope.assignment.Permit()
	if permit.WorkerGeneration() != generation {
		return
	}
	spec := envelope.assignment.Spec()
	started := time.Now().UTC()
	_ = envelope.events.Started(permit, started)

	baseCtx, cancelBase := context.WithCancel(envelope.ctx)
	defer cancelBase()
	stopExecutorLink := context.AfterFunc(executorCtx, cancelBase)
	defer stopExecutorLink()
	runCtx := baseCtx
	cancelRun := func() {}
	if timeout := spec.ExecutionTimeout(); timeout > 0 {
		runCtx, cancelRun = context.WithTimeout(baseCtx, timeout)
	}
	defer cancelRun()

	outcome := tasks.OutcomeSucceeded
	cause := tasks.ResultCauseNone
	failure := tasks.FailureInfo{}
	var output tasks.ResultRef

	handler, ok := e.resolver.ResolveHandler(spec.Handler())
	if !ok {
		outcome = tasks.OutcomeFailed
		cause = tasks.ResultCauseHandlerError
		failure = tasks.FailureInfo{Code: "unknown_handler", Message: "handler is not registered"}
	} else {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					outcome = tasks.OutcomeFailed
					cause = tasks.ResultCausePanic
					failure = tasks.FailureInfo{Code: "panic", Message: fmt.Sprint(recovered)}
				}
			}()
			var err error
			output, err = handler(runCtx, spec.Input())
			if err == nil {
				return
			}
			switch {
			case errors.Is(runCtx.Err(), context.DeadlineExceeded):
				outcome = tasks.OutcomeTimedOut
				cause = tasks.ResultCauseExecutionTimeout
			case errors.Is(runCtx.Err(), context.Canceled):
				outcome = tasks.OutcomeCancelled
				cause = tasks.ResultCauseCancellation
			default:
				outcome = tasks.OutcomeFailed
				cause = tasks.ResultCauseHandlerError
			}
			failure = tasks.FailureInfo{Code: "handler_error", Message: err.Error()}
		}()
	}

	finished := time.Now().UTC()
	result, err := tasks.NewTaskResult(tasks.TaskResultParams{
		TaskID: spec.ID(), Outcome: outcome, Cause: cause, StartedAt: started,
		FinishedAt: finished, Output: output, Failure: failure,
	})
	// The handler has returned, so the physical slot is genuinely reusable.
	// Release it before publishing completion; TaskEngine cannot assign it until
	// it processes that event, which eliminates the completion/busy race.
	releaseSlot()
	if err == nil {
		_ = envelope.events.Completed(permit, result)
	}
}
