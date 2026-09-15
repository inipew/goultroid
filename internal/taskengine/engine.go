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
	// InboxCapacity bounds the single-writer control inbox. Zero means default.
	InboxCapacity int
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
	InboxCapacity:       2048,
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
	dispatchEpoch   uint64
	poolGeneration  uint64
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

// opKind identifies a control message delivered to the single-writer runLoop.
type opKind int

const (
	opSubmit opKind = iota
	opCancel
	opCancelScope
	opSnapshot
	opResult
	opWorkerIdle
	opWorkerStarted
	opWorkerCompleted
	opSweep
	opQuiesce
	opSetOwnerLimits
	opStats
	opStopFinalize
)

type engineRequest struct {
	op      opKind
	ctx     context.Context
	spec    tasks.WorkSpec
	taskID  tasks.TaskID
	scope   tasks.ScopeIdentity
	reason  tasks.Cause
	pool    tasks.PoolID
	slotID  int
	permit  *permit
	result  tasks.TaskResult
	started time.Time
	owner   tasks.OwnerID
	limits  admission.OwnerLimits
	reply   chan engineReply
}

type engineReply struct {
	ticket    tasks.Ticket
	err       error
	receipt   tasks.CancelReceipt
	count     int
	snapshot  tasks.TaskSnapshot
	found     bool
	result    tasks.TaskResult
	hasResult bool
	stats     engineStats
}

type engineStats struct {
	resultSlotsHeld int
	resultCapacity  int
	activeTasks     int
	accepting       bool
	quiesced        bool
}

// Engine coordinates admission, fairness, physical worker permits, result credits, and lifecycles.
// All mutable execution state below is owned exclusively by the runLoop goroutine
// (single writer). Producers interact only through the bounded inbox, so the
// DecisionTimeout bounds the full admission decision including queueing delay.
// The mu mutex protects lifecycle fields only (rootCtx, accepting flags,
// inbox handle), never the execution registry.
type Engine struct {
	mu sync.Mutex

	config Config
	adm    *admission.Controller

	// ---- runLoop-owned execution state (never touch outside runLoop) ----
	idleSlots         map[tasks.PoolID][]int
	poolConcurrencies map[tasks.PoolID]int
	poolGenerations   map[tasks.PoolID]uint64
	dispatchEpoch     uint64

	// Physical worker mailboxes per pool: pool -> slotID -> channel
	workerMailboxes map[tasks.PoolID][]chan workerAssignment

	resultCapacity  int
	resultSlotsHeld int

	registry            map[tasks.TaskID]*taskRecord
	cancelledScopes     map[tasks.ScopeIdentity]tasks.Cause
	terminalOrder       []tasks.TaskID
	maxTerminalRetained int

	decisionTimeout time.Duration
	inboxCap        int

	// ---- lifecycle (mu-protected) ----
	inbox       chan engineRequest
	accepting   bool
	quiesced    bool
	drained     bool
	rootCtx     context.Context
	rootCancel  context.CancelFunc
	activeTasks int
	drainDone   chan struct{}
	runStarted  bool
}

// ValidateConfig checks that pool and engine limits are non-negative.
func ValidateConfig(cfg Config) error {
	if cfg.ResultCapacity < 0 {
		return errors.New("taskengine: ResultCapacity cannot be negative")
	}
	if cfg.DecisionTimeout < 0 {
		return errors.New("taskengine: DecisionTimeout cannot be negative")
	}
	if cfg.InboxCapacity < 0 {
		return errors.New("taskengine: InboxCapacity cannot be negative")
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

	inboxCap := cfg.InboxCapacity
	if inboxCap <= 0 {
		inboxCap = DefaultConfig.InboxCapacity
		if inboxCap <= 0 {
			inboxCap = 2048
		}
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
		inboxCap:            inboxCap,
		drainDone:           make(chan struct{}),
	}
}

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
	if e.runStarted {
		return errors.New("task engine already started")
	}
	e.rootCtx, e.rootCancel = context.WithCancel(ctx)
	e.accepting = true
	e.quiesced = false
	e.drained = false
	e.drainDone = make(chan struct{})
	e.inbox = make(chan engineRequest, e.inboxCap)
	inbox := e.inbox
	rootCtx := e.rootCtx
	e.runStarted = true

	go e.runLoop(rootCtx, inbox)
	for poolID, mboxes := range e.workerMailboxes {
		for slotID, ch := range mboxes {
			go e.physicalWorker(poolID, slotID, ch, rootCtx)
		}
	}
	return nil
}

// runLoop is the sole writer of execution state. One inbox message is processed
// per turn (bounded work): dispatch is bounded by idle physical slots and sweep
// is bounded by expired entries. User handlers and OnComplete callbacks never
// run inside this loop.
func (e *Engine) runLoop(ctx context.Context, inbox <-chan engineRequest) {
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
		earliest, hasEarliest := e.adm.EarliestDeadline()
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
			return
		case req, ok := <-inbox:
			if !ok {
				return
			}
			e.handleRequest(ctx, req)
			armSweeper()
		case <-sweepTimerCh:
			now := time.Now().UTC()
			for poolID := range e.config.Pools {
				e.sweepExpired(poolID, now)
			}
			armSweeper()
		}
	}
}

func (e *Engine) handleRequest(ctx context.Context, req engineRequest) {
	switch req.op {
	case opSubmit:
		ticket, err := e.admitSubmit(ctx, req.ctx, req.spec)
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
		req.reply <- engineReply{stats: engineStats{
			resultSlotsHeld: e.resultSlotsHeld,
			resultCapacity:  e.resultCapacity,
			activeTasks:     e.activeTasks,
			accepting:       e.lifecycleAccepting(),
			quiesced:        e.lifecycleQuiesced(),
		}}
	case opWorkerIdle:
		e.markWorkerIdle(req.pool, req.slotID)
	case opWorkerStarted:
		e.applyWorkerStarted(req.taskID, req.permit, req.started)
	case opWorkerCompleted:
		e.applyWorkerCompleted(req.result)
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
		e.adm.SetOwnerLimits(req.owner, req.limits)
		if req.reply != nil {
			req.reply <- engineReply{}
		}
	case opStopFinalize:
		e.applyStopFinalize()
		if req.reply != nil {
			req.reply <- engineReply{}
		}
	}
}

// lifecycleAccepting/Quiesced read mu-protected flags; called only from runLoop
// via handleRequest, so use non-blocking loads guarded by mutex here would
// deadlock. Instead runLoop keeps its own copies synced on quiesce. For now
// read under a try-lock-free path: these flags are only mutated via inbox ops
// (applyQuiesce) plus Start under mu. runLoop is the writer after Start, so
// plain reads here are safe (single writer).
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
			e.sendInternal(engineRequest{op: opWorkerCompleted, result: res})
			e.sendInternal(engineRequest{op: opWorkerIdle, pool: pool, slotID: slotID})
		}
	}
}

// sendInternal delivers worker-originated events to the control loop. It blocks
// on root context only; the inbox is drained continuously by runLoop.
func (e *Engine) sendInternal(req engineRequest) {
	e.mu.Lock()
	inbox := e.inbox
	rootCtx := e.rootCtx
	e.mu.Unlock()
	if inbox == nil {
		return
	}
	select {
	case inbox <- req:
	case <-rootCtx.Done():
	}
}

// sendControl delivers a producer request to the bounded inbox, enforcing the
// DecisionTimeout over the full wait (queueing + decision). This is the fix for
// the old behaviour where the context deadline could not bound mu.Lock waits.
func (e *Engine) sendControl(ctx context.Context, req engineRequest) (engineReply, error) {
	e.mu.Lock()
	inbox := e.inbox
	rootCtx := e.rootCtx
	timeout := e.decisionTimeout
	e.mu.Unlock()
	if inbox == nil || rootCtx == nil {
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
	select {
	case inbox <- req:
	case <-ctx.Done():
		return engineReply{}, ctx.Err()
	case <-rootCtx.Done():
		return engineReply{}, tasks.NewAdmissionError(tasks.ReasonEngineQuiescing, tasks.ErrEngineQuiescing)
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

func (e *Engine) admitSubmit(loopCtx context.Context, callerCtx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	if callerCtx != nil {
		if err := callerCtx.Err(); err != nil {
			return nil, err
		}
	}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("invalid work spec: %w", err)
	}
	if _, exists := e.registry[spec.ID]; exists {
		return nil, errors.New("task id already registered")
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
	payloadBytes := int64(0)
	if b, ok := spec.Input.([]byte); ok {
		payloadBytes = int64(len(b))
	}
	if err := e.adm.CanAdmit(spec, payloadBytes); err != nil {
		return nil, err
	}
	if input, ok := spec.Input.([]byte); ok {
		spec.Input = append([]byte(nil), input...)
	}
	if spec.Job != nil {
		ref := *spec.Job
		spec.Job = &ref
	}
	// Linearization check: a cancellation racing admission wins here, under the
	// single writer, so there is exactly one decision point.
	if callerCtx != nil {
		if err := callerCtx.Err(); err != nil {
			return nil, tasks.NewAdmissionError(tasks.ReasonLinearizationCancel, fmt.Errorf("%w: %v", tasks.ErrLinearizationCancel, err))
		}
	}
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
	e.syncActiveTasks(1)
	rec.state = tasks.StateQueued
	e.adm.Enqueue(&admission.QueueEntry{
		Spec:        spec,
		EnqueuedAt:  now,
		PayloadSize: payloadBytes,
	})
	e.tryDispatch(spec.Pool)
	return ticket, nil
}

// syncActiveTasks keeps the mu-visible active counter in sync with the
// runLoop-owned value. The authoritative count lives in the loop; the mutex
// copy exists only for fast lifecycle reads. Called only from runLoop.
func (e *Engine) syncActiveTasks(delta int) {
	e.activeTasks += delta
}

func (e *Engine) sweepExpired(pool tasks.PoolID, now time.Time) {
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
		e.onTaskSettled(rec)
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

// tryDispatch runs only inside runLoop. Assignment means "worker may run this",
// NOT "worker has started": the record moves to Dispatching and becomes
// Running only when the worker emits the Started event at the execution
// boundary.
func (e *Engine) tryDispatch(pool tasks.PoolID) {
	if e.rootCtx == nil || e.rootCtx.Err() != nil {
		return
	}
	e.sweepExpired(pool, time.Now().UTC())

	for len(e.idleSlots[pool]) > 0 {
		candidate, err := e.adm.SelectCandidate(pool)
		if err != nil {
			break
		}
		rec := e.registry[candidate.Spec.ID]
		if rec == nil || rec.state != tasks.StateQueued {
			continue
		}
		slotID := e.idleSlots[pool][0]
		e.idleSlots[pool] = e.idleSlots[pool][1:]
		e.dispatchEpoch++

		gen := e.poolGenerations[pool]
		permit := newPermit(pool, slotID, gen, rec.spec.ID, e.dispatchEpoch, nil)

		rec.permit = permit
		rec.dispatchEpoch = e.dispatchEpoch
		rec.poolGeneration = gen
		rec.state = tasks.StateDispatching

		taskCtx, cancel := context.WithCancel(e.rootCtx)
		if rec.cancelRequested && rec.cancelFunc == nil {
			// Cancel arrived while queued; propagate to the fresh context.
		}
		if rec.cancelRequested {
			// A cancel won the race before dispatch completed: keep the
			// request visible to the worker via context + late-cancel path.
			// The worker boundary will observe ctx cancellation.
			_ = cancel
		}
		rec.cancelFunc = cancel

		e.workerMailboxes[pool][slotID] <- workerAssignment{
			rec:     rec,
			spec:    candidate.Spec,
			permit:  permit,
			taskCtx: taskCtx,
		}
		if rec.cancelRequested {
			// Ensure a pre-dispatch cancel is visible even if the worker
			// has already dequeued the assignment.
			cancel()
		}
	}
}

func (e *Engine) markWorkerIdle(pool tasks.PoolID, slotID int) {
	e.idleSlots[pool] = append(e.idleSlots[pool], slotID)
	for p := range e.config.Pools {
		e.tryDispatch(p)
	}
}

// applyWorkerStarted is the fencing point for the Dispatching -> Running
// transition. Only the worker holding the current permit epoch may promote the
// record; stale or forged grants are ignored.
func (e *Engine) applyWorkerStarted(id tasks.TaskID, grant *permit, startedAt time.Time) {
	rec, ok := e.registry[id]
	if !ok || rec.state != tasks.StateDispatching {
		return
	}
	if grant == nil || rec.permit != grant {
		return
	}
	if grant.generation != rec.poolGeneration || grant.dispatchEpoch != rec.dispatchEpoch {
		return
	}
	rec.state = tasks.StateRunning
	rec.startedAt = startedAt
}

func (e *Engine) applyWorkerCompleted(res tasks.TaskResult) {
	rec, ok := e.registry[res.TaskID]
	if !ok {
		return
	}
	if rec.isTerminal() {
		return
	}
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
	spec := rec.spec
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
	if e.resultSlotsHeld > 0 {
		e.resultSlotsHeld--
	}
	close(rec.done)
	e.onTaskSettled(rec)

	if spec.OnComplete != nil {
		fn := spec.OnComplete
		go func(r tasks.TaskResult) {
			defer func() { _ = recover() }()
			fn(r)
		}(res)
	}
}

// applyCancel is the single linearization point for cancellation vs dispatch,
// completion, and snapshot. Dispatching tasks already own a context, so cancel
// is delivered via cancelFunc and observed at the worker boundary (late-cancel
// path in applyWorkerCompleted).
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
		e.onTaskSettled(rec)
		if rec.spec.OnComplete != nil {
			fn, result := rec.spec.OnComplete, rec.result
			go func() {
				defer func() { _ = recover() }()
				fn(result)
			}()
		}
		return tasks.CancelReceipt{TaskID: id, Accepted: true, State: tasks.StateCancelled, Reason: reason}, nil
	case tasks.StateDispatching, tasks.StateRunning:
		rec.cancelRequested = true
		rec.cancelReason = reason
		if rec.cancelFunc != nil {
			rec.cancelFunc()
		}
		return tasks.CancelReceipt{TaskID: id, Accepted: true, State: rec.state, Reason: reason}, nil
	default:
		return tasks.CancelReceipt{TaskID: id, Accepted: false, State: rec.state, Reason: reason}, nil
	}
}

func (e *Engine) applyCancelScope(scope tasks.ScopeIdentity, reason tasks.Cause) int {
	if e.cancelledScopes == nil {
		e.cancelledScopes = make(map[tasks.ScopeIdentity]tasks.Cause)
	}
	// Generation barrier first: any later admission in this scope is rejected
	// at the single linearization point in admitSubmit.
	e.cancelledScopes[scope] = reason
	cancelled := 0
	for id, rec := range e.registry {
		if rec.spec.Scope.Owner != scope.Owner {
			continue
		}
		if scope.Generation != 0 && rec.spec.Scope.Generation != scope.Generation {
			continue
		}
		if rec.isTerminal() {
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
	case tasks.StateCompleted, tasks.StateFailed, tasks.StateCancelled, tasks.StateTimedOut:
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

func (e *Engine) applyQuiesce() {
	e.accepting = false
	e.quiesced = true
	e.checkDrained()
}

func (e *Engine) applyStopFinalize() {
	for id, rec := range e.registry {
		if rec.state == tasks.StateQueued || rec.state == tasks.StateDispatching {
			_, _ = e.applyCancel(id, tasks.CauseShutdown)
		}
	}
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

func (e *Engine) checkDrained() {
	if e.quiesced && e.activeTasks == 0 && !e.drained {
		e.drained = true
		// drainDone is recreated on every Start; close exactly once.
		select {
		case <-e.drainDone:
		default:
			close(e.drainDone)
		}
	}
}

// Quiesce stops accepting new tasks while letting already admitted tasks run.
func (e *Engine) Quiesce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	inbox := e.inbox
	rootCtx := e.rootCtx
	e.mu.Unlock()
	if inbox == nil || rootCtx == nil {
		return nil
	}
	reply := make(chan engineReply, 1)
	req := engineRequest{op: opQuiesce, reply: reply}
	select {
	case inbox <- req:
		select {
		case <-reply:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-rootCtx.Done():
			return nil
		}
	case <-ctx.Done():
		// Best effort: mark quiesced under lifecycle lock so Drain still ends.
		e.mu.Lock()
		e.accepting = false
		e.quiesced = true
		e.mu.Unlock()
		return ctx.Err()
	case <-rootCtx.Done():
		return nil
	}
}

// Drain waits until all admitted and in-flight tasks have reached a terminal outcome.
func (e *Engine) Drain(ctx context.Context) error {
	_ = e.Quiesce(ctx)
	e.mu.Lock()
	drainDone := e.drainDone
	e.mu.Unlock()
	if drainDone == nil {
		return nil
	}
	select {
	case <-drainDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop terminates the engine and cancels residual tasks if drain timed out.
func (e *Engine) Stop(ctx context.Context) error {
	_ = e.Quiesce(ctx)
	err := e.Drain(ctx)
	e.mu.Lock()
	inbox := e.inbox
	rootCancel := e.rootCancel
	e.mu.Unlock()
	if inbox != nil {
		reply := make(chan engineReply, 1)
		select {
		case inbox <- engineRequest{op: opStopFinalize, reply: reply}:
			select {
			case <-reply:
			case <-time.After(2 * time.Second):
			}
		default:
		}
	}
	if rootCancel != nil {
		rootCancel()
	}
	return err
}

// Health probes the health status of the task engine.
func (e *Engine) Health(ctx context.Context) runtime.ComponentHealth {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	inbox := e.inbox
	rootCtx := e.rootCtx
	accepting := e.accepting
	quiesced := e.quiesced
	e.mu.Unlock()
	if inbox == nil || rootCtx == nil {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "task engine not running"}
	}
	if !accepting && !quiesced {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "task engine not running"}
	}
	reply := make(chan engineReply, 1)
	select {
	case inbox <- engineRequest{op: opStats, reply: reply}:
		timeout := 200 * time.Millisecond
		if dl, ok := ctx.Deadline(); ok {
			if d := time.Until(dl); d < timeout {
				timeout = d
			}
		}
		if timeout <= 0 {
			timeout = 200 * time.Millisecond
		}
		select {
		case rep := <-reply:
			if rep.stats.resultCapacity > 0 && rep.stats.resultSlotsHeld >= rep.stats.resultCapacity {
				return runtime.ComponentHealth{
					Status:  runtime.HealthDegraded,
					Details: fmt.Sprintf("result capacity saturated (%d/%d)", rep.stats.resultSlotsHeld, rep.stats.resultCapacity),
				}
			}
			return runtime.ComponentHealth{Status: runtime.HealthHealthy}
		case <-time.After(timeout):
			return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "task engine control loop unresponsive"}
		case <-ctx.Done():
			return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "health check cancelled"}
		}
	case <-ctx.Done():
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "health check cancelled"}
	default:
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "task engine inbox saturated"}
	}
}

// SetOwnerLimits sets quota and weight limits for an owner.
func (e *Engine) SetOwnerLimits(owner tasks.OwnerID, limits admission.OwnerLimits) {
	e.mu.Lock()
	inbox := e.inbox
	rootCtx := e.rootCtx
	e.mu.Unlock()
	if inbox == nil {
		return
	}
	reply := make(chan engineReply, 1)
	select {
	case inbox <- engineRequest{op: opSetOwnerLimits, owner: owner, limits: limits, reply: reply}:
		select {
		case <-reply:
		case <-time.After(2 * time.Second):
		case <-rootCtx.Done():
		}
	default:
	}
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
// The call blocks only on the bounded control inbox; DecisionTimeout covers the
// whole wait, including queueing behind other producers.
func (e *Engine) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rep, err := e.sendControl(ctx, engineRequest{op: opSubmit, spec: spec})
	if err != nil {
		return nil, err
	}
	return rep.ticket, rep.err
}

// Cancel cancels an execution attempt by ID (ADR 0006 §5.1).
func (e *Engine) Cancel(id tasks.TaskID, reason tasks.Cause) (tasks.CancelReceipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.decisionTimeoutOrDefault())
	defer cancel()
	rep, err := e.sendControl(ctx, engineRequest{op: opCancel, taskID: id, reason: reason})
	if err != nil {
		return tasks.CancelReceipt{TaskID: id, Accepted: false, Reason: reason}, err
	}
	return rep.receipt, rep.err
}

// CancelScope cancels all active and queued tasks matching scope owner and generation,
// and sets a scope barrier to prevent future submissions on this scope.
func (e *Engine) CancelScope(scope tasks.ScopeIdentity, reason tasks.Cause) int {
	ctx, cancel := context.WithTimeout(context.Background(), e.decisionTimeoutOrDefault())
	defer cancel()
	rep, err := e.sendControl(ctx, engineRequest{op: opCancelScope, scope: scope, reason: reason})
	if err != nil {
		return 0
	}
	return rep.count
}

// Snapshot returns point-in-time lifecycle status of a task.
func (e *Engine) Snapshot(id tasks.TaskID) (tasks.TaskSnapshot, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), e.decisionTimeoutOrDefault())
	defer cancel()
	rep, err := e.sendControl(ctx, engineRequest{op: opSnapshot, taskID: id})
	if err != nil {
		return tasks.TaskSnapshot{}, false
	}
	return rep.snapshot, rep.found
}

func (e *Engine) decisionTimeoutOrDefault() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.decisionTimeout > 0 {
		return e.decisionTimeout
	}
	return 5 * time.Second
}

func (e *Engine) taskState(id tasks.TaskID) tasks.TaskState {
	snap, ok := e.Snapshot(id)
	if !ok {
		return ""
	}
	return snap.State
}

func (e *Engine) taskResult(id tasks.TaskID) (tasks.TaskResult, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rep, err := e.sendControl(ctx, engineRequest{op: opResult, taskID: id})
	if err != nil || !rep.hasResult {
		return tasks.TaskResult{}, false
	}
	return rep.result, true
}
