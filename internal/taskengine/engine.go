package taskengine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
)

// Ensure Engine implements tasks.Client and runtime.Component.
var (
	_ tasks.Client      = (*Engine)(nil)
	_ runtime.Component = (*Engine)(nil)
)

// PoolEngineConfig sets concurrency and queue parameters for a pool in TaskEngine.
type PoolEngineConfig struct {
	Concurrency   int
	BacklogLimit  int
	PayloadBudget int64
}

// Config configures the central execution coordinator (ADR 0006 §3.1).
type Config struct {
	Pools               map[tasks.PoolID]PoolEngineConfig
	ResultCapacity      int
	MaxTerminalRetained int
	DecisionTimeout     time.Duration
}

// DefaultConfig provides standard execution coordinator settings.
var DefaultConfig = Config{
	Pools: map[tasks.PoolID]PoolEngineConfig{
		"general":       {Concurrency: 8, BacklogLimit: 200, PayloadBudget: 100 * 1024 * 1024},
		"interactive":   {Concurrency: 32, BacklogLimit: 128, PayloadBudget: 50 * 1024 * 1024},
		"download":      {Concurrency: 3, BacklogLimit: 50, PayloadBudget: 200 * 1024 * 1024},
		"media-process": {Concurrency: 2, BacklogLimit: 20, PayloadBudget: 200 * 1024 * 1024},
		"scheduler":     {Concurrency: 4, BacklogLimit: 100, PayloadBudget: 50 * 1024 * 1024},
	},
	ResultCapacity:      1000,
	MaxTerminalRetained: 1000,
	DecisionTimeout:     5 * time.Second,
}

type workerAssignment struct {
	rec     *taskRecord
	spec    tasks.WorkSpec
	permit  *permit
	taskCtx context.Context
}

type taskRecord struct {
	spec            tasks.WorkSpec
	state           tasks.TaskState
	permit          *permit
	result          tasks.TaskResult
	done            chan struct{}
	ticket          *engineTicket
	cancelFunc      context.CancelFunc
	cancelRequested bool
	cancelReason    tasks.Cause

	admittedAt time.Time
	queuedAt   time.Time
	startedAt  time.Time
	finishedAt time.Time
	errorMsg   string
}

func (r *taskRecord) isTerminal() bool {
	if r == nil {
		return false
	}
	return r.state == tasks.StateCompleted || r.state == tasks.StateFailed || r.state == tasks.StateTimedOut || r.state == tasks.StateCancelled
}

// Engine coordinates admission, fairness, physical worker permits, result credits, and lifecycles.
// It is the single owner of mutable execution state across all pools (ADR 0006 §3.1).
type Engine struct {
	mu sync.Mutex

	config Config
	adm    *admission.Controller

	// Physical slots inventory per pool: pool -> idle workerIDs
	idleSlots         map[tasks.PoolID][]int
	poolConcurrencies map[tasks.PoolID]int
	poolGenerations   map[tasks.PoolID]uint64
	dispatchEpoch     uint64

	// Physical worker mailboxes per pool: pool -> slotID -> channel
	workerMailboxes map[tasks.PoolID][]chan workerAssignment

	// Result capacity reservation
	resultCapacity  int
	resultSlotsHeld int

	// Task registry & memory retention
	registry            map[tasks.TaskID]*taskRecord
	cancelledScopes     map[tasks.ScopeIdentity]tasks.Cause
	terminalOrder       []tasks.TaskID
	maxTerminalRetained int

	// Dynamic deadline sweeper wake
	wakeSweeper chan struct{}

	// Decision timeout
	decisionTimeout time.Duration

	// Lifecycle
	accepting   bool
	quiesced    bool
	drained     bool
	rootCtx     context.Context
	rootCancel  context.CancelFunc
	activeTasks int
	drainDone   chan struct{}
}

// ValidateConfig checks that pool and engine limits are non-negative.
func ValidateConfig(cfg Config) error {
	if cfg.ResultCapacity < 0 {
		return errors.New("taskengine: ResultCapacity cannot be negative")
	}
	if cfg.DecisionTimeout < 0 {
		return errors.New("taskengine: DecisionTimeout cannot be negative")
	}
	for poolID, pcfg := range cfg.Pools {
		if poolID == "" {
			return errors.New("taskengine: pool ID cannot be empty")
		}
		if pcfg.Concurrency < 0 {
			return fmt.Errorf("taskengine: pool %s concurrency cannot be negative", poolID)
		}
		if pcfg.BacklogLimit < 0 {
			return fmt.Errorf("taskengine: pool %s backlog limit cannot be negative", poolID)
		}
		if pcfg.PayloadBudget < 0 {
			return fmt.Errorf("taskengine: pool %s payload budget cannot be negative", poolID)
		}
	}
	return nil
}

// NewEngine constructs a TaskEngine with the specified configuration.
func NewEngine(cfg Config) *Engine {
	if cfg.ResultCapacity <= 0 {
		cfg.ResultCapacity = DefaultConfig.ResultCapacity
	}
	poolsSource := cfg.Pools
	if len(poolsSource) == 0 {
		poolsSource = DefaultConfig.Pools
	}

	// Defensive copy of pools to isolate engine configuration from caller mutation
	copiedPools := make(map[tasks.PoolID]PoolEngineConfig, len(poolsSource))
	for k, v := range poolsSource {
		copiedPools[k] = v
	}
	cfg.Pools = copiedPools

	admPoolConfigs := make(map[tasks.PoolID]admission.PoolConfig, len(cfg.Pools))
	idleSlots := make(map[tasks.PoolID][]int, len(cfg.Pools))
	concurrencies := make(map[tasks.PoolID]int, len(cfg.Pools))
	generations := make(map[tasks.PoolID]uint64, len(cfg.Pools))

	maxTerminal := cfg.MaxTerminalRetained
	if maxTerminal <= 0 {
		maxTerminal = 1000
	}

	decisionTimeout := cfg.DecisionTimeout
	if decisionTimeout <= 0 {
		decisionTimeout = 5 * time.Second
	}

	mailboxes := make(map[tasks.PoolID][]chan workerAssignment, len(cfg.Pools))
	for poolID, pcfg := range cfg.Pools {
		if pcfg.Concurrency <= 0 {
			pcfg.Concurrency = 4
		}
		concurrencies[poolID] = pcfg.Concurrency
		generations[poolID] = 1

		slots := make([]int, pcfg.Concurrency)
		mboxes := make([]chan workerAssignment, pcfg.Concurrency)
		for i := 0; i < pcfg.Concurrency; i++ {
			slots[i] = i
			mboxes[i] = make(chan workerAssignment, 1)
		}
		idleSlots[poolID] = slots
		mailboxes[poolID] = mboxes

		admPoolConfigs[poolID] = admission.PoolConfig{
			BacklogLimit:  pcfg.BacklogLimit,
			PayloadBudget: pcfg.PayloadBudget,
		}
	}

	return &Engine{
		config:              cfg,
		adm:                 admission.NewController(admPoolConfigs),
		idleSlots:           idleSlots,
		poolConcurrencies:   concurrencies,
		poolGenerations:     generations,
		workerMailboxes:     mailboxes,
		resultCapacity:      cfg.ResultCapacity,
		registry:            make(map[tasks.TaskID]*taskRecord),
		cancelledScopes:     make(map[tasks.ScopeIdentity]tasks.Cause),
		maxTerminalRetained: maxTerminal,
		decisionTimeout:     decisionTimeout,
		wakeSweeper:         make(chan struct{}, 1),
		drainDone:           make(chan struct{}),
	}
}

func (e *Engine) Name() string           { return "taskengine" }
func (e *Engine) Dependencies() []string { return nil }

// Start initializes the engine lifecycle and starts fixed physical worker loops.
func (e *Engine) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.rootCtx != nil {
		return errors.New("task engine already started")
	}
	e.rootCtx, e.rootCancel = context.WithCancel(ctx)
	e.accepting = true
	e.quiesced = false
	e.drained = false

	for poolID, mboxes := range e.workerMailboxes {
		for slotID, ch := range mboxes {
			go e.physicalWorker(poolID, slotID, ch, e.rootCtx)
		}
	}

	go e.sweepLoop(e.rootCtx)
	return nil
}

func (e *Engine) physicalWorker(pool tasks.PoolID, slotID int, mailbox <-chan workerAssignment, ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case assignment, ok := <-mailbox:
			if !ok {
				return
			}
			e.executeAssignment(assignment.rec, assignment.spec, assignment.permit, assignment.taskCtx)
			e.onWorkerIdle(pool, slotID)
		}
	}
}

func (e *Engine) onWorkerIdle(pool tasks.PoolID, slotID int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.idleSlots[pool] = append(e.idleSlots[pool], slotID)
	for p := range e.config.Pools {
		e.tryDispatchLocked(p)
	}
}

func (e *Engine) sweepLoop(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	defer timer.Stop()

	for {
		e.mu.Lock()
		now := time.Now().UTC()
		for poolID := range e.config.Pools {
			e.sweepExpiredLocked(poolID, now)
		}
		earliest, hasEarliest := e.adm.EarliestDeadline()
		e.mu.Unlock()

		if !hasEarliest {
			select {
			case <-ctx.Done():
				return
			case <-e.wakeSweeper:
				continue
			}
		}

		delay := time.Until(earliest)
		if delay < 0 {
			delay = 0
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)

		select {
		case <-ctx.Done():
			return
		case <-e.wakeSweeper:
		case <-timer.C:
		}
	}
}

// Quiesce stops accepting new tasks while letting already admitted tasks run.
func (e *Engine) Quiesce(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.accepting = false
	e.quiesced = true
	e.checkDrainedLocked()
	return nil
}

// Drain waits until all admitted and in-flight tasks have reached a terminal outcome.
func (e *Engine) Drain(ctx context.Context) error {
	_ = e.Quiesce(ctx)

	select {
	case <-e.drainDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) checkDrainedLocked() {
	if e.quiesced && e.activeTasks == 0 && !e.drained {
		e.drained = true
		close(e.drainDone)
	}
}

func (e *Engine) taskSettledLocked(rec *taskRecord) {
	e.activeTasks--
	if rec != nil && rec.isTerminal() {
		e.terminalOrder = append(e.terminalOrder, rec.spec.ID)
		e.evictTerminalRecordsLocked()
	}
	e.checkDrainedLocked()
}

func (e *Engine) evictTerminalRecordsLocked() {
	if e.maxTerminalRetained <= 0 {
		return
	}
	for len(e.terminalOrder) > e.maxTerminalRetained {
		oldestID := e.terminalOrder[0]
		e.terminalOrder[0] = ""
		e.terminalOrder = e.terminalOrder[1:]
		if oldRec, ok := e.registry[oldestID]; ok && oldRec.isTerminal() {
			delete(e.registry, oldestID)
		}
	}
}

// Stop terminates the engine and cancels residual tasks if drain timed out.
func (e *Engine) Stop(ctx context.Context) error {
	_ = e.Quiesce(ctx)
	err := e.Drain(ctx)
	e.mu.Lock()
	if e.rootCancel != nil {
		e.rootCancel()
	}
	var pending []tasks.TaskID
	for id, rec := range e.registry {
		if rec.state == tasks.StateQueued {
			pending = append(pending, id)
		}
	}
	e.mu.Unlock()
	for _, id := range pending {
		_, _ = e.Cancel(id, tasks.CauseShutdown)
	}
	return err
}

// Health probes the health status of the task engine.
func (e *Engine) Health(ctx context.Context) runtime.ComponentHealth {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepting && !e.quiesced {
		return runtime.ComponentHealth{
			Status:  runtime.HealthDegraded,
			Details: "task engine not running",
		}
	}
	if e.resultCapacity > 0 && e.resultSlotsHeld >= e.resultCapacity {
		return runtime.ComponentHealth{
			Status:  runtime.HealthDegraded,
			Details: fmt.Sprintf("result capacity saturated (%d/%d)", e.resultSlotsHeld, e.resultCapacity),
		}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}

// SetOwnerLimits sets quota and weight limits for an owner.
func (e *Engine) SetOwnerLimits(owner tasks.OwnerID, limits admission.OwnerLimits) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.adm.SetOwnerLimits(owner, limits)
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

// Submit validates, reserves capacity, and enqueues work into the coordinator (ADR 0006 §5.1).
func (e *Engine) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := e.decisionTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("invalid work spec: %w", err)
	}

	cell := &submitCell{}

	e.mu.Lock()
	defer e.mu.Unlock()

	if err := ctx.Err(); err != nil {
		cell.state.Store(uint32(decisionCancelled))
		return nil, err
	}
	if _, exists := e.registry[spec.ID]; exists {
		cell.state.Store(uint32(decisionRejected))
		return nil, errors.New("task id already registered")
	}
	if spec.Handler == nil {
		cell.state.Store(uint32(decisionRejected))
		return nil, tasks.NewAdmissionError(tasks.ReasonUnknownHandler, tasks.ErrUnknownHandler)
	}
	if !spec.QueueDeadline.IsZero() && !time.Now().Before(spec.QueueDeadline) {
		cell.state.Store(uint32(decisionRejected))
		return nil, tasks.NewAdmissionError(tasks.ReasonDeadlineExpired, tasks.ErrDeadlineExpired)
	}

	if !e.accepting {
		cell.state.Store(uint32(decisionRejected))
		return nil, tasks.NewAdmissionError(tasks.ReasonEngineQuiescing, tasks.ErrEngineQuiescing)
	}

	if spec.Scope.Owner != "" {
		if cause, closed := e.cancelledScopes[spec.Scope]; closed {
			cell.state.Store(uint32(decisionRejected))
			return nil, tasks.NewAdmissionError(tasks.ReasonScopeClosed, fmt.Errorf("%w: scope %s (generation %d) is closed (%s)", tasks.ErrScopeClosed, spec.Scope.Owner, spec.Scope.Generation, cause))
		}
		if cause, closed := e.cancelledScopes[tasks.ScopeIdentity{Owner: spec.Scope.Owner, Generation: 0}]; closed {
			cell.state.Store(uint32(decisionRejected))
			return nil, tasks.NewAdmissionError(tasks.ReasonScopeClosed, fmt.Errorf("%w: scope %s is closed (%s)", tasks.ErrScopeClosed, spec.Scope.Owner, cause))
		}
	}

	// 1. Result capacity reservation check
	if e.resultCapacity > 0 && e.resultSlotsHeld >= e.resultCapacity {
		cell.state.Store(uint32(decisionRejected))
		return nil, tasks.NewAdmissionError(tasks.ReasonResultBackpressure, tasks.ErrResultBackpressure)
	}

	// 2. Admission policy check (backlog, payload budget, owner quota)
	payloadBytes := int64(0)
	if b, ok := spec.Input.([]byte); ok {
		payloadBytes = int64(len(b))
	}
	if err := e.adm.CanAdmit(spec, payloadBytes); err != nil {
		cell.state.Store(uint32(decisionRejected))
		return nil, err
	}

	if input, ok := spec.Input.([]byte); ok {
		spec.Input = append([]byte(nil), input...)
	}
	if spec.Job != nil {
		ref := *spec.Job
		spec.Job = &ref
	}

	// Linearization cancellation check right before publication
	if err := ctx.Err(); err != nil {
		cell.state.Store(uint32(decisionCancelled))
		return nil, tasks.NewAdmissionError(tasks.ReasonLinearizationCancel, fmt.Errorf("%w: %v", tasks.ErrLinearizationCancel, err))
	}

	// 3. Atomically register task
	now := time.Now().UTC()
	doneCh := make(chan struct{})
	rec := &taskRecord{
		spec:       spec,
		state:      tasks.StateAdmitted,
		done:       doneCh,
		admittedAt: now,
		queuedAt:   now,
	}
	ticket := &engineTicket{
		taskID: spec.ID,
		engine: e,
		done:   doneCh,
		rec:    rec,
	}
	rec.ticket = ticket
	e.registry[spec.ID] = rec
	e.resultSlotsHeld++
	e.activeTasks++

	// Transition to Queued and enqueue into ready queues
	rec.state = tasks.StateQueued
	e.adm.Enqueue(&admission.QueueEntry{
		Spec:        spec,
		EnqueuedAt:  now,
		PayloadSize: payloadBytes,
	})

	cell.state.Store(uint32(decisionAccepted))

	if !spec.QueueDeadline.IsZero() {
		select {
		case e.wakeSweeper <- struct{}{}:
		default:
		}
	}

	// Attempt physical dispatch if slots are idle
	e.tryDispatchLocked(spec.Pool)

	return ticket, nil
}

func (e *Engine) sweepExpiredLocked(pool tasks.PoolID, now time.Time) {
	expired := e.adm.PopExpired(pool, now)
	for _, entry := range expired {
		rec, ok := e.registry[entry.Spec.ID]
		if !ok || rec.state != tasks.StateQueued {
			continue
		}
		rec.state = tasks.StateTimedOut
		rec.finishedAt = now
		rec.errorMsg = "queue deadline expired before execution"
		rec.result = tasks.TaskResult{
			TaskID:     entry.Spec.ID,
			Outcome:    tasks.OutcomeTimedOut,
			Cause:      tasks.CauseQueueExpired,
			FinishedAt: now,
			Failure: tasks.FailureInfo{
				Message: rec.errorMsg,
			},
		}
		if e.resultSlotsHeld > 0 {
			e.resultSlotsHeld--
		}
		close(rec.done)
		e.taskSettledLocked(rec)
		if rec.spec.OnComplete != nil {
			fn := rec.spec.OnComplete
			res := rec.result
			go func() {
				defer func() { _ = recover() }()
				fn(res)
			}()
		}
	}
}

func (e *Engine) tryDispatchLocked(pool tasks.PoolID) {
	if e.rootCtx == nil || e.rootCtx.Err() != nil {
		return
	}
	e.sweepExpiredLocked(pool, time.Now().UTC())

	for len(e.idleSlots[pool]) > 0 {
		candidate, err := e.adm.SelectCandidate(pool)
		if err != nil {
			break
		}

		rec := e.registry[candidate.Spec.ID]
		if rec == nil || rec.state != tasks.StateQueued {
			continue
		}

		// Allocate physical slot
		slotID := e.idleSlots[pool][0]
		e.idleSlots[pool] = e.idleSlots[pool][1:]
		e.dispatchEpoch++

		gen := e.poolGenerations[pool]
		permit := newPermit(pool, slotID, gen, rec.spec.ID, e.dispatchEpoch, nil)

		rec.permit = permit
		rec.state = tasks.StateRunning
		rec.startedAt = time.Now().UTC()

		taskCtx, cancel := context.WithCancel(e.rootCtx)
		rec.cancelFunc = cancel

		e.workerMailboxes[pool][slotID] <- workerAssignment{
			rec:     rec,
			spec:    candidate.Spec,
			permit:  permit,
			taskCtx: taskCtx,
		}
	}
}

func (e *Engine) executeAssignment(rec *taskRecord, spec tasks.WorkSpec, permit *permit, taskCtx context.Context) {
	res := executeAssignment(taskCtx, spec, permit)

	e.mu.Lock()
	defer e.mu.Unlock()

	rec.result = res
	rec.finishedAt = res.FinishedAt
	if rec.cancelRequested && res.Outcome != tasks.OutcomeCancelled {
		res.Outcome = tasks.OutcomeCancelled
		res.Cause = rec.cancelReason
		if res.Failure.Message == "" {
			res.Failure.Message = fmt.Sprintf("late cancellation applied: %s", rec.cancelReason)
		}
		rec.result = res
	}
	switch res.Outcome {
	case tasks.OutcomeCompleted:
		rec.state = tasks.StateCompleted
	case tasks.OutcomeTimedOut:
		rec.state = tasks.StateTimedOut
		rec.errorMsg = res.Failure.Message
	case tasks.OutcomeCancelled:
		rec.state = tasks.StateCancelled
		rec.errorMsg = res.Failure.Message
	default:
		rec.state = tasks.StateFailed
		rec.errorMsg = res.Failure.Message
	}

	e.adm.OnTaskTerminal(spec)
	if rec.cancelFunc != nil {
		rec.cancelFunc()
	}
	rec.startedAt = res.StartedAt
	// Owner and ordering constraints are global, so every pool may now be eligible.
	for pool := range e.config.Pools {
		e.tryDispatchLocked(pool)
	}
	if e.resultSlotsHeld > 0 {
		e.resultSlotsHeld--
	}
	close(rec.done)
	e.taskSettledLocked(rec)

	if rec.spec.OnComplete != nil {
		fn := rec.spec.OnComplete
		go func(r tasks.TaskResult) {
			defer func() { _ = recover() }()
			fn(r)
		}(res)
	}
}

// Cancel cancels an execution attempt by ID (ADR 0006 §5.1).
func (e *Engine) Cancel(id tasks.TaskID, reason tasks.Cause) (tasks.CancelReceipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	rec, exists := e.registry[id]
	if !exists {
		return tasks.CancelReceipt{TaskID: id, Accepted: false, State: "", Reason: reason}, tasks.ErrTaskNotFound
	}

	switch rec.state {
	case tasks.StateCreated, tasks.StateAdmitted, tasks.StateQueued:
		e.adm.RemoveTask(id)
		rec.state = tasks.StateCancelled
		rec.finishedAt = time.Now().UTC()
		rec.result = tasks.TaskResult{
			TaskID:     id,
			Outcome:    tasks.OutcomeCancelled,
			Cause:      reason,
			FinishedAt: rec.finishedAt,
		}
		if e.resultSlotsHeld > 0 {
			e.resultSlotsHeld--
		}
		close(rec.done)
		e.taskSettledLocked(rec)
		if rec.spec.OnComplete != nil {
			fn, result := rec.spec.OnComplete, rec.result
			go func() {
				defer func() { _ = recover() }()
				fn(result)
			}()
		}
		return tasks.CancelReceipt{TaskID: id, Accepted: true, State: tasks.StateCancelled, Reason: reason}, nil

	case tasks.StateRunning:
		rec.cancelRequested = true
		rec.cancelReason = reason
		if rec.cancelFunc != nil {
			rec.cancelFunc()
		}
		return tasks.CancelReceipt{TaskID: id, Accepted: true, State: tasks.StateRunning, Reason: reason}, nil

	default:
		// Terminal state
		return tasks.CancelReceipt{TaskID: id, Accepted: false, State: rec.state, Reason: reason}, nil
	}
}

// CancelScope cancels all active and queued tasks matching scope owner and generation,
// and sets a scope barrier to prevent future submissions on this scope.
func (e *Engine) CancelScope(scope tasks.ScopeIdentity, reason tasks.Cause) int {
	e.mu.Lock()
	if e.cancelledScopes == nil {
		e.cancelledScopes = make(map[tasks.ScopeIdentity]tasks.Cause)
	}
	e.cancelledScopes[scope] = reason
	var toCancel []tasks.TaskID
	for id, rec := range e.registry {
		if rec.spec.Scope.Owner == scope.Owner &&
			(scope.Generation == 0 || rec.spec.Scope.Generation == scope.Generation) &&
			rec.state != tasks.StateCompleted &&
			rec.state != tasks.StateFailed &&
			rec.state != tasks.StateCancelled &&
			rec.state != tasks.StateTimedOut {
			toCancel = append(toCancel, id)
		}
	}
	e.mu.Unlock()

	cancelled := 0
	for _, id := range toCancel {
		if receipt, err := e.Cancel(id, reason); err == nil && receipt.Accepted {
			cancelled++
		}
	}
	return cancelled
}

// Snapshot returns point-in-time lifecycle status of a task.
func (e *Engine) Snapshot(id tasks.TaskID) (tasks.TaskSnapshot, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	rec, ok := e.registry[id]
	if !ok {
		return tasks.TaskSnapshot{}, false
	}

	return tasks.TaskSnapshot{
		ID:         rec.spec.ID,
		Scope:      rec.spec.Scope,
		QuotaOwner: rec.spec.QuotaOwner,
		Pool:       rec.spec.Pool,
		Class:      rec.spec.Class,
		State:      rec.state,
		AdmittedAt: rec.admittedAt,
		QueuedAt:   rec.queuedAt,
		StartedAt:  rec.startedAt,
		FinishedAt: rec.finishedAt,
		Error:      rec.errorMsg,
	}, true
}

func (e *Engine) taskState(id tasks.TaskID) tasks.TaskState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rec, ok := e.registry[id]; ok {
		return rec.state
	}
	return ""
}

func (e *Engine) taskResult(id tasks.TaskID) (tasks.TaskResult, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rec, ok := e.registry[id]; ok {
		if rec.state == tasks.StateCompleted ||
			rec.state == tasks.StateFailed ||
			rec.state == tasks.StateCancelled ||
			rec.state == tasks.StateTimedOut {
			return rec.result, true
		}
	}
	return tasks.TaskResult{}, false
}
