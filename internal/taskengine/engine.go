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
	Concurrency int
	// MinConcurrency enables adaptive sizing when positive and lower than
	// Concurrency. Zero preserves the historical fixed-size pool behavior.
	MinConcurrency int
	// IdleTimeout retires adaptive workers above MinConcurrency. Zero uses the
	// default timeout.
	IdleTimeout   time.Duration
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
	// MaxRetainedBytes caps admitted-but-unevicted memory (payload + bounded
	// output + failure text + per-record overhead). Zero means default.
	MaxRetainedBytes int64
	// MaxOutputBytes caps a single stored TaskResult.Output. Zero = default.
	MaxOutputBytes int64
	// MaxFailureBytes caps one stored failure message/detail. Zero = default.
	MaxFailureBytes int
	// DeliveryConcurrency is the fixed completion-callback worker count.
	// Zero means default.
	DeliveryConcurrency int
	// DeliveryQueueCap bounds both callback reservations and pending callback
	// delivery. Zero means max(ResultCapacity, 256).
	DeliveryQueueCap int
	// TerminalTTL evicts terminal records older than the TTL on the sweep
	// path. Zero disables TTL eviction (count/byte eviction still applies).
	TerminalTTL time.Duration
	// MaxScopeTombstones bounds revoked scope generations retained to fence
	// late submissions. Zero means the default.
	MaxScopeTombstones int
	// ResourceCapacities defines dispatch-time resource budgets by stable name.
	ResourceCapacities map[string]int64
}

// DefaultConfig provides standard execution coordinator settings.
var DefaultConfig = Config{
	Pools: map[tasks.PoolID]PoolEngineConfig{
		"general":       {Concurrency: 8, MinConcurrency: 1, IdleTimeout: 30 * time.Second, BacklogLimit: 200, PayloadBudget: 100 * 1024 * 1024},
		"interactive":   {Concurrency: 32, MinConcurrency: 2, IdleTimeout: 30 * time.Second, BacklogLimit: 128, PayloadBudget: 50 * 1024 * 1024},
		"download":      {Concurrency: 3, MinConcurrency: 1, IdleTimeout: 45 * time.Second, BacklogLimit: 50, PayloadBudget: 200 * 1024 * 1024},
		"media-process": {Concurrency: 2, MinConcurrency: 1, IdleTimeout: time.Minute, BacklogLimit: 20, PayloadBudget: 200 * 1024 * 1024},
		"scheduler":     {Concurrency: 4, MinConcurrency: 1, IdleTimeout: time.Minute, BacklogLimit: 100, PayloadBudget: 50 * 1024 * 1024},
	},
	ResultCapacity:      1000,
	MaxTerminalRetained: 1000,
	DecisionTimeout:     5 * time.Second,
	InboxCapacity:       2048,
	MaxRetainedBytes:    DefaultMaxRetainedBytes,
	MaxOutputBytes:      DefaultMaxOutputBytes,
	MaxFailureBytes:     DefaultMaxFailureBytes,
	DeliveryConcurrency: DefaultDeliveryConcurrency,
	MaxScopeTombstones:  4096,
}

type workerAssignment struct {
	rec     *taskRecord
	spec    tasks.WorkSpec
	permit  *permit
	taskCtx context.Context
}

// HandlerResolver turns a stable handler reference plus immutable input into
// the physical execution closure. Resolution happens during admission.
type HandlerResolver interface {
	ResolveHandler(string, any) (tasks.HandlerFunc, error)
}

type taskRecord struct {
	spec             tasks.WorkSpec
	state            tasks.TaskState
	permit           *permit
	dispatchEpoch    uint64
	poolGeneration   uint64
	result           tasks.TaskResult
	done             chan struct{}
	ticket           *engineTicket
	cancelFunc       context.CancelFunc
	cancelRequested  bool
	cancelReason     tasks.Cause
	callbackReserved bool

	// retainedBytes is the accountable memory owned by this record
	// (payload + bounded output + failure text + overhead), released at eviction.
	retainedBytes int64

	// Durability phase (Phase C).
	durability    durabilityState
	pendingResult tasks.TaskResult
	execOutcome   tasks.Outcome
	commitSeq     uint64

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
	return r.state == tasks.StateCompleted || r.state == tasks.StateFailed || r.state == tasks.StateTimedOut || r.state == tasks.StateCancelled || r.state == tasks.StateRecoveryRequired
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
	opWorkerRetire
	opWorkerStarted
	opWorkerCompleted
	opCommitAck
	opSweep
	opQuiesce
	opSetOwnerLimits
	opConfigurePool
	opSetResourceCapacity
	opStats
	opStopFinalize
)

type engineRequest struct {
	op       opKind
	ctx      context.Context
	spec     tasks.WorkSpec
	taskID   tasks.TaskID
	scope    tasks.ScopeIdentity
	reason   tasks.Cause
	pool     tasks.PoolID
	slotID   int
	permit   *permit
	result   tasks.TaskResult
	started  time.Time
	decision *submitCell
	// commitSeq + ackErr carry the fenced durability acknowledgement.
	commitSeq        uint64
	ackErr           error
	owner            tasks.OwnerID
	limits           admission.OwnerLimits
	poolConfig       PoolEngineConfig
	resourceName     string
	resourceCapacity int64
	reply            chan engineReply
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
	retainedBytes   int64
	retainedCap     int64
	terminalCount   int
	commitPending   int
	deliveryQueued  int
	deliveryCap     int
	deliveryFailed  int64
	durabilityQueue int
	durabilityCap   int
	durabilityFail  int64
	scopeTombstones int
	pools           map[tasks.PoolID]PoolRuntimeStats
	resources       map[string]ResourceRuntimeStats
}

type PoolRuntimeStats struct {
	Workers      int
	MinWorkers   int
	MaxWorkers   int
	Idle         int
	IdleWorkers  int
	Running      int
	Dispatching  int
	Waiting      int
	WaitingBytes int64
}

type ResourceRuntimeStats struct{ Used, Capacity int64 }

// RuntimeStats is a bounded-cardinality snapshot of execution coordination.
type RuntimeStats struct {
	ResultSlotsHeld int
	ResultCapacity  int
	ActiveTasks     int
	RetainedBytes   int64
	RetainedCap     int64
	TerminalCount   int
	CommitPending   int
	DeliveryQueued  int
	DeliveryCap     int
	DeliveryFailed  int64
	DurabilityQueue int
	DurabilityCap   int
	DurabilityFail  int64
	ScopeTombstones int
	Pools           map[tasks.PoolID]PoolRuntimeStats
	Resources       map[string]ResourceRuntimeStats
}

// Engine coordinates admission, fairness, physical worker permits, result credits, and lifecycles.
// All mutable execution state below is owned exclusively by the runLoop goroutine.
type Engine struct {
	mu sync.Mutex

	config    Config
	configErr error
	adm       *admission.Controller

	// ---- runLoop-owned execution state ----
	idleSlots         map[tasks.PoolID][]int
	poolConcurrencies map[tasks.PoolID]int
	poolMinWorkers    map[tasks.PoolID]int
	poolIdleTimeouts  map[tasks.PoolID]time.Duration
	workerRunning     map[tasks.PoolID][]bool
	poolGenerations   map[tasks.PoolID]uint64
	resourceCapacity  map[string]int64
	resourceUsed      map[string]int64
	dispatchEpoch     uint64

	workerMailboxes map[tasks.PoolID][]chan workerAssignment

	resultCapacity  int
	resultSlotsHeld int

	registry            map[tasks.TaskID]*taskRecord
	cancelledScopes     map[tasks.ScopeIdentity]tasks.Cause
	cancelledScopeOrder []tasks.ScopeIdentity
	maxScopeTombstones  int
	terminalOrder       []tasks.TaskID
	maxTerminalRetained int

	// ---- runLoop-owned memory accounting ----
	retainedBytes    int64
	maxRetainedBytes int64
	maxOutputBytes   int64
	maxFailureBytes  int
	terminalTTL      time.Duration

	// ---- runLoop-owned durability ----
	commitPump      CommitPump
	handlerResolver HandlerResolver
	commitSeq       uint64
	commitPending   int
	commitWaiters   map[uint64]context.CancelFunc

	decisionTimeout time.Duration
	inboxCap        int

	// ---- lifecycle handles ----
	inbox       chan engineRequest
	delivery    *completionDelivery
	durability  *durabilityLane
	accepting   bool
	quiesced    bool
	drained     bool
	rootCtx     context.Context
	rootCancel  context.CancelFunc
	activeTasks int
	drainDone   chan struct{}
	runStarted  bool

	runtimeRemaining atomic.Int64
	runtimeDone      chan struct{}
	runtimeDoneOnce  sync.Once
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
	if cfg.MaxRetainedBytes < 0 {
		return errors.New("taskengine: MaxRetainedBytes cannot be negative")
	}
	if cfg.MaxOutputBytes < 0 {
		return errors.New("taskengine: MaxOutputBytes cannot be negative")
	}
	if cfg.MaxFailureBytes < 0 {
		return errors.New("taskengine: MaxFailureBytes cannot be negative")
	}
	if cfg.DeliveryConcurrency < 0 {
		return errors.New("taskengine: DeliveryConcurrency cannot be negative")
	}
	if cfg.DeliveryQueueCap < 0 {
		return errors.New("taskengine: DeliveryQueueCap cannot be negative")
	}
	if cfg.TerminalTTL < 0 {
		return errors.New("taskengine: TerminalTTL cannot be negative")
	}
	if cfg.MaxScopeTombstones < 0 {
		return errors.New("taskengine: MaxScopeTombstones cannot be negative")
	}
	for poolID, pcfg := range cfg.Pools {
		if poolID == "" {
			return errors.New("taskengine: pool ID cannot be empty")
		}
		if pcfg.Concurrency < 0 {
			return fmt.Errorf("taskengine: pool %s concurrency cannot be negative", poolID)
		}
		if pcfg.MinConcurrency < 0 || (pcfg.Concurrency > 0 && pcfg.MinConcurrency > pcfg.Concurrency) {
			return fmt.Errorf("taskengine: pool %s minimum concurrency is invalid", poolID)
		}
		if pcfg.IdleTimeout < 0 {
			return fmt.Errorf("taskengine: pool %s idle timeout cannot be negative", poolID)
		}
		if pcfg.BacklogLimit < 0 {
			return fmt.Errorf("taskengine: pool %s backlog limit cannot be negative", poolID)
		}
		if pcfg.PayloadBudget < 0 {
			return fmt.Errorf("taskengine: pool %s payload budget cannot be negative", poolID)
		}
	}
	for name, capacity := range cfg.ResourceCapacities {
		if name == "" || capacity <= 0 {
			return errors.New("taskengine: resource capacities need a name and positive capacity")
		}
	}
	return nil
}

// NewEngine constructs a TaskEngine with the specified configuration.
func NewEngine(cfg Config) *Engine {
	configErr := ValidateConfig(cfg)
	if cfg.ResultCapacity <= 0 {
		cfg.ResultCapacity = DefaultConfig.ResultCapacity
	}
	poolsSource := cfg.Pools
	if len(poolsSource) == 0 {
		poolsSource = DefaultConfig.Pools
	}
	copiedPools := make(map[tasks.PoolID]PoolEngineConfig, len(poolsSource))
	for k, v := range poolsSource {
		copiedPools[k] = v
	}
	cfg.Pools = copiedPools
	resourceCapacities := make(map[string]int64, len(cfg.ResourceCapacities))
	for name, capacity := range cfg.ResourceCapacities {
		resourceCapacities[name] = capacity
	}
	cfg.ResourceCapacities = resourceCapacities

	admPoolConfigs := make(map[tasks.PoolID]admission.PoolConfig, len(cfg.Pools))
	idleSlots := make(map[tasks.PoolID][]int, len(cfg.Pools))
	concurrencies := make(map[tasks.PoolID]int, len(cfg.Pools))
	minimums := make(map[tasks.PoolID]int, len(cfg.Pools))
	idleTimeouts := make(map[tasks.PoolID]time.Duration, len(cfg.Pools))
	workerRunning := make(map[tasks.PoolID][]bool, len(cfg.Pools))
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
	maxRetained := cfg.MaxRetainedBytes
	if maxRetained <= 0 {
		maxRetained = DefaultMaxRetainedBytes
	}
	maxOutput := cfg.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = DefaultMaxOutputBytes
	}
	maxFailure := cfg.MaxFailureBytes
	if maxFailure <= 0 {
		maxFailure = DefaultMaxFailureBytes
	}
	maxScopeTombstones := cfg.MaxScopeTombstones
	if maxScopeTombstones <= 0 {
		maxScopeTombstones = DefaultConfig.MaxScopeTombstones
	}
	deliveryWorkers := cfg.DeliveryConcurrency
	if deliveryWorkers <= 0 {
		deliveryWorkers = DefaultDeliveryConcurrency
	}
	deliveryCap := cfg.DeliveryQueueCap
	if deliveryCap <= 0 {
		deliveryCap = cfg.ResultCapacity
		if deliveryCap < 256 {
			deliveryCap = 256
		}
	}

	mailboxes := make(map[tasks.PoolID][]chan workerAssignment, len(cfg.Pools))
	for poolID, pcfg := range cfg.Pools {
		if pcfg.Concurrency <= 0 {
			pcfg.Concurrency = 4
		}
		concurrencies[poolID] = pcfg.Concurrency
		minimum := pcfg.MinConcurrency
		if minimum <= 0 {
			minimum = pcfg.Concurrency
		}
		minimums[poolID] = minimum
		idleTimeout := pcfg.IdleTimeout
		if idleTimeout <= 0 {
			idleTimeout = 30 * time.Second
		}
		idleTimeouts[poolID] = idleTimeout
		generations[poolID] = 1
		slots := make([]int, pcfg.Concurrency)
		mboxes := make([]chan workerAssignment, pcfg.Concurrency)
		for i := 0; i < pcfg.Concurrency; i++ {
			slots[i] = i
			mboxes[i] = make(chan workerAssignment, 1)
		}
		idleSlots[poolID] = slots[:0]
		mailboxes[poolID] = mboxes
		workerRunning[poolID] = make([]bool, pcfg.Concurrency)
		admPoolConfigs[poolID] = admission.PoolConfig{BacklogLimit: pcfg.BacklogLimit, PayloadBudget: pcfg.PayloadBudget}
	}

	return &Engine{
		config:              cfg,
		adm:                 admission.NewController(admPoolConfigs),
		idleSlots:           idleSlots,
		poolConcurrencies:   concurrencies,
		poolMinWorkers:      minimums,
		poolIdleTimeouts:    idleTimeouts,
		workerRunning:       workerRunning,
		poolGenerations:     generations,
		resourceCapacity:    resourceCapacities,
		resourceUsed:        make(map[string]int64, len(resourceCapacities)),
		workerMailboxes:     mailboxes,
		resultCapacity:      cfg.ResultCapacity,
		registry:            make(map[tasks.TaskID]*taskRecord),
		cancelledScopes:     make(map[tasks.ScopeIdentity]tasks.Cause),
		maxScopeTombstones:  maxScopeTombstones,
		maxTerminalRetained: maxTerminal,
		maxRetainedBytes:    maxRetained,
		maxOutputBytes:      maxOutput,
		maxFailureBytes:     maxFailure,
		terminalTTL:         cfg.TerminalTTL,
		decisionTimeout:     decisionTimeout,
		inboxCap:            inboxCap,
		delivery:            newCompletionDelivery(deliveryWorkers, deliveryCap),
		durability:          newDurabilityLane(defaultDurabilityConcurrency, cfg.ResultCapacity),
		drainDone:           make(chan struct{}),
		runtimeDone:         make(chan struct{}),
		configErr:           configErr,
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
	e.inbox = make(chan engineRequest, e.inboxCap)
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
	go func() {
		defer e.runtimeLoopDone()
		e.runLoop(rootCtx, inbox)
	}()
	for poolID := range e.workerMailboxes {
		for slotID := 0; slotID < e.poolMinWorkers[poolID]; slotID++ {
			if e.spawnWorker(poolID, slotID, rootCtx) {
				e.idleSlots[poolID] = append(e.idleSlots[poolID], slotID)
			}
		}
	}
	return nil
}

func (e *Engine) spawnWorker(pool tasks.PoolID, slot int, ctx context.Context) bool {
	running := e.workerRunning[pool]
	if slot < 0 || slot >= len(running) || running[slot] {
		return false
	}
	running[slot] = true
	e.runtimeRemaining.Add(1)
	mailbox := e.workerMailboxes[pool][slot]
	idleTimeout := e.poolIdleTimeouts[pool]
	go func() {
		defer e.runtimeLoopDone()
		e.physicalWorker(pool, slot, mailbox, idleTimeout, ctx)
	}()
	return true
}

func (e *Engine) runtimeLoopDone() {
	if e.runtimeRemaining.Add(-1) == 0 {
		e.runtimeDoneOnce.Do(func() { close(e.runtimeDone) })
	}
}

// runLoop is the sole writer of execution state.
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
			e.applyStopFinalize()
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
	case opWorkerIdle:
		e.markWorkerIdle(req.pool, req.slotID)
	case opWorkerRetire:
		retired := e.retireIdleWorker(req.pool, req.slotID)
		if req.reply != nil {
			req.reply <- engineReply{found: retired}
		}
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

func (e *Engine) physicalWorker(pool tasks.PoolID, slotID int, mailbox <-chan workerAssignment, idleTimeout time.Duration, ctx context.Context) {
	if idleTimeout <= 0 {
		idleTimeout = 30 * time.Second
	}
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if e.requestWorkerRetire(ctx, pool, slotID) {
				return
			}
			timer.Reset(idleTimeout)
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
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idleTimeout)
		}
	}
}

func (e *Engine) requestWorkerRetire(ctx context.Context, pool tasks.PoolID, slot int) bool {
	e.mu.Lock()
	inbox := e.inbox
	root := e.rootCtx
	e.mu.Unlock()
	if inbox == nil || root == nil {
		return true
	}
	reply := make(chan engineReply, 1)
	select {
	case inbox <- engineRequest{op: opWorkerRetire, pool: pool, slotID: slot, reply: reply}:
	case <-ctx.Done():
		return true
	case <-root.Done():
		return true
	}
	select {
	case result := <-reply:
		return result.found
	case <-ctx.Done():
		return true
	case <-root.Done():
		return true
	}
}

// sendInternal delivers worker-originated events to the control loop.
func (e *Engine) sendInternal(req engineRequest) {
	e.mu.Lock()
	inbox := e.inbox
	rootCtx := e.rootCtx
	e.mu.Unlock()
	if inbox == nil || rootCtx == nil {
		return
	}
	select {
	case inbox <- req:
	case <-rootCtx.Done():
	}
}

// sendControl handles non-submit producer requests. Submit has a stronger
// decision-cell protocol below because cancellation after inbox handoff must
// not hide an admission decision.
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

// sendSubmitControl makes the inbox handoff explicit. Before handoff, caller
// cancellation simply prevents submission. After handoff, caller cancellation
// may win only by atomically fencing the still-pending coordinator decision.
// If the coordinator has already published accepted/rejected, Submit waits for
// that reply even if the caller context expires, eliminating ambiguous
// "returned error but task was accepted" outcomes.
func (e *Engine) sendSubmitControl(ctx context.Context, spec tasks.WorkSpec) (engineReply, error) {
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
	decision := &submitCell{}
	reply := make(chan engineReply, 1)
	req := engineRequest{op: opSubmit, ctx: ctx, spec: spec, decision: decision, reply: reply}
	select {
	case inbox <- req:
	case <-ctx.Done():
		return engineReply{}, ctx.Err()
	case <-rootCtx.Done():
		return engineReply{}, tasks.NewAdmissionError(tasks.ReasonEngineQuiescing, tasks.ErrEngineQuiescing)
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

func (e *Engine) syncActiveTasks(delta int) { e.activeTasks += delta }

// boundResult enforces output/failure caps on a terminal result.
func (e *Engine) boundResult(res tasks.TaskResult) (tasks.TaskResult, int64) {
	out, outBytes := capOutput(res.Output, e.maxOutputBytes)
	res.Output = out
	res.Failure.Message = truncateField(res.Failure.Message, e.maxFailureBytes)
	res.Failure.Detail = truncateField(res.Failure.Detail, e.maxFailureBytes)
	return res, outBytes + int64(len(res.Failure.Message)+len(res.Failure.Detail))
}

// settleTerminal performs the shared terminal tail for every completion path.
func (e *Engine) settleTerminal(rec *taskRecord) {
	if e.resultSlotsHeld > 0 {
		e.resultSlotsHeld--
	}
	if rec.spec.OnComplete != nil && rec.callbackReserved {
		_ = e.delivery.enqueueReserved(rec.spec.OnComplete, rec.result)
		rec.callbackReserved = false
	}
	close(rec.done)
	e.onTaskSettled(rec)
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
		rec.result = tasks.TaskResult{TaskID: entry.Spec.ID, Outcome: tasks.OutcomeTimedOut, Cause: tasks.CauseQueueExpired, FinishedAt: now, Failure: tasks.FailureInfo{Message: rec.errorMsg}}
		bounded, delta := e.boundResult(rec.result)
		rec.result = bounded
		rec.retainedBytes += delta
		e.retainedBytes += delta
		e.settleTerminal(rec)
	}
	e.evictExpiredTerminal(now)
}

// tryDispatch assigns queued work to idle physical slots.
func (e *Engine) tryDispatch(pool tasks.PoolID) {
	if e.rootCtx == nil || e.rootCtx.Err() != nil {
		return
	}
	e.sweepExpired(pool, time.Now().UTC())
	if waiting, _ := e.adm.PoolStats(pool); waiting > 0 && len(e.idleSlots[pool]) == 0 {
		e.spawnNextWorker(pool)
	}
	for len(e.idleSlots[pool]) > 0 {
		candidate, err := e.adm.SelectCandidateEligible(pool, e.resourcesAvailable)
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
		e.reserveResources(rec.spec)

		taskCtx, cancel := context.WithCancel(e.rootCtx)
		rec.cancelFunc = cancel
		e.workerMailboxes[pool][slotID] <- workerAssignment{rec: rec, spec: candidate.Spec, permit: permit, taskCtx: taskCtx}
		if rec.cancelRequested {
			cancel()
		}
	}
	if waiting, _ := e.adm.PoolStats(pool); waiting > 0 && len(e.idleSlots[pool]) == 0 {
		e.spawnNextWorker(pool)
	}
}

func (e *Engine) spawnNextWorker(pool tasks.PoolID) bool {
	runningCount := 0
	for _, running := range e.workerRunning[pool] {
		if running {
			runningCount++
		}
	}
	if runningCount >= e.poolConcurrencies[pool] {
		return false
	}
	for slot, running := range e.workerRunning[pool] {
		if !running {
			if e.spawnWorker(pool, slot, e.rootCtx) {
				e.idleSlots[pool] = append(e.idleSlots[pool], slot)
				return true
			}
			return false
		}
	}
	return false
}

func (e *Engine) applyPoolConfig(pool tasks.PoolID, cfg PoolEngineConfig) error {
	hardMax := len(e.workerMailboxes[pool])
	if hardMax == 0 {
		return fmt.Errorf("taskengine: unknown pool %s", pool)
	}
	if cfg.Concurrency <= 0 || cfg.Concurrency > hardMax || cfg.MinConcurrency < 0 || cfg.MinConcurrency > cfg.Concurrency {
		return errors.New("taskengine: invalid live pool bounds")
	}
	if cfg.MinConcurrency == 0 {
		cfg.MinConcurrency = 1
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = e.poolIdleTimeouts[pool]
	}
	if err := e.adm.SetPoolConfig(pool, admission.PoolConfig{BacklogLimit: cfg.BacklogLimit, PayloadBudget: cfg.PayloadBudget}); err != nil {
		return err
	}
	e.poolConcurrencies[pool] = cfg.Concurrency
	e.poolMinWorkers[pool] = cfg.MinConcurrency
	e.poolIdleTimeouts[pool] = cfg.IdleTimeout
	for runningCount(e.workerRunning[pool]) < cfg.MinConcurrency {
		if !e.spawnNextWorker(pool) {
			break
		}
	}
	return nil
}

func runningCount(slots []bool) int {
	count := 0
	for _, running := range slots {
		if running {
			count++
		}
	}
	return count
}

func (e *Engine) applyResourceCapacity(name string, capacity int64) error {
	if name == "" || capacity <= 0 {
		return errors.New("taskengine: resource name and positive capacity required")
	}
	if used := e.resourceUsed[name]; capacity < used {
		return fmt.Errorf("taskengine: resource %s currently uses %d", name, used)
	}
	e.resourceCapacity[name] = capacity
	for pool := range e.config.Pools {
		e.tryDispatch(pool)
	}
	return nil
}

func (e *Engine) resourcesAvailable(spec tasks.WorkSpec) bool {
	for _, requirement := range spec.Resources {
		if e.resourceUsed[requirement.Name]+requirement.Amount > e.resourceCapacity[requirement.Name] {
			return false
		}
	}
	return true
}

func (e *Engine) reserveResources(spec tasks.WorkSpec) {
	for _, requirement := range spec.Resources {
		e.resourceUsed[requirement.Name] += requirement.Amount
	}
}

func (e *Engine) releaseResources(spec tasks.WorkSpec) {
	for _, requirement := range spec.Resources {
		e.resourceUsed[requirement.Name] -= requirement.Amount
		if e.resourceUsed[requirement.Name] <= 0 {
			delete(e.resourceUsed, requirement.Name)
		}
	}
}

func (e *Engine) markWorkerIdle(pool tasks.PoolID, slotID int) {
	if slotID < 0 || slotID >= len(e.workerRunning[pool]) || !e.workerRunning[pool][slotID] {
		return
	}
	for _, existing := range e.idleSlots[pool] {
		if existing == slotID {
			return
		}
	}
	e.idleSlots[pool] = append(e.idleSlots[pool], slotID)
	for p := range e.config.Pools {
		e.tryDispatch(p)
	}
}

func (e *Engine) retireIdleWorker(pool tasks.PoolID, slotID int) bool {
	runningCount := 0
	for _, running := range e.workerRunning[pool] {
		if running {
			runningCount++
		}
	}
	if runningCount <= e.poolMinWorkers[pool] {
		return false
	}
	index := -1
	for i, idle := range e.idleSlots[pool] {
		if idle == slotID {
			index = i
			break
		}
	}
	if index < 0 {
		return false
	}
	e.idleSlots[pool] = append(e.idleSlots[pool][:index], e.idleSlots[pool][index+1:]...)
	e.workerRunning[pool][slotID] = false
	return true
}

// applyWorkerStarted is the fencing point for Dispatching -> Running.
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

// SetCommitPump installs the durable-commit transport. It must be called before Start.
func (e *Engine) SetCommitPump(p CommitPump) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runStarted {
		return
	}
	e.commitPump = p
}

// SetHandlerResolver installs the stable-reference resolver before Start.
func (e *Engine) SetHandlerResolver(resolver HandlerResolver) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.runStarted {
		e.handlerResolver = resolver
	}
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
	if rec.cancelRequested && res.Outcome != tasks.OutcomeCancelled {
		res.Outcome = tasks.OutcomeCancelled
		res.Cause = rec.cancelReason
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
		rec.result = tasks.TaskResult{TaskID: id, Outcome: tasks.OutcomeCancelled, Cause: reason, FinishedAt: rec.finishedAt}
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
		StartedAt: rec.startedAt, FinishedAt: now,
		Failure: tasks.FailureInfo{Message: failureMessage},
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
		return ctx.Err()
	case <-rootCtx.Done():
		return nil
	}
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

// Health probes the health status of the task engine.
// Stats returns aggregate execution diagnostics through the coordinator so the
// snapshot is internally consistent.
func (e *Engine) Stats(ctx context.Context) (RuntimeStats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rep, err := e.sendControl(ctx, engineRequest{op: opStats, reply: make(chan engineReply, 1)})
	if err != nil {
		return RuntimeStats{}, err
	}
	s := rep.stats
	return RuntimeStats{
		ResultSlotsHeld: s.resultSlotsHeld, ResultCapacity: s.resultCapacity,
		ActiveTasks: s.activeTasks, RetainedBytes: s.retainedBytes, RetainedCap: s.retainedCap,
		TerminalCount: s.terminalCount, CommitPending: s.commitPending,
		DeliveryQueued: s.deliveryQueued, DeliveryCap: s.deliveryCap, DeliveryFailed: s.deliveryFailed,
		DurabilityQueue: s.durabilityQueue, DurabilityCap: s.durabilityCap, DurabilityFail: s.durabilityFail,
		ScopeTombstones: s.scopeTombstones,
		Pools:           s.pools, Resources: s.resources,
	}, nil
}

func (e *Engine) Health(ctx context.Context) runtime.ComponentHealth {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	inbox := e.inbox
	rootCtx := e.rootCtx
	e.mu.Unlock()
	if inbox == nil || rootCtx == nil {
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
				return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: fmt.Sprintf("result capacity saturated (%d/%d)", rep.stats.resultSlotsHeld, rep.stats.resultCapacity)}
			}
			if rep.stats.retainedCap > 0 && rep.stats.retainedBytes >= rep.stats.retainedCap {
				return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: fmt.Sprintf("retained memory saturated (%d/%d bytes)", rep.stats.retainedBytes, rep.stats.retainedCap)}
			}
			if rep.stats.deliveryCap > 0 && rep.stats.deliveryQueued >= rep.stats.deliveryCap {
				return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: fmt.Sprintf("completion delivery saturated (%d/%d)", rep.stats.deliveryQueued, rep.stats.deliveryCap)}
			}
			if rep.stats.deliveryFailed > 0 {
				return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: fmt.Sprintf("completion delivery invariant failures: %d", rep.stats.deliveryFailed)}
			}
			if rep.stats.durabilityCap > 0 && rep.stats.durabilityQueue >= rep.stats.durabilityCap {
				return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: fmt.Sprintf("durability acknowledgement lane saturated (%d/%d)", rep.stats.durabilityQueue, rep.stats.durabilityCap)}
			}
			if rep.stats.durabilityFail > 0 {
				return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: fmt.Sprintf("durability lane failures: %d", rep.stats.durabilityFail)}
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
	if inbox == nil || rootCtx == nil {
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
	case <-rootCtx.Done():
	}
}

// ConfigurePool adjusts an adaptive pool within the startup hard maximum.
func (e *Engine) ConfigurePool(ctx context.Context, pool tasks.PoolID, cfg PoolEngineConfig) error {
	reply, err := e.sendControl(ctx, engineRequest{op: opConfigurePool, pool: pool, poolConfig: cfg})
	if err != nil {
		return err
	}
	return reply.err
}

// SetResourceCapacity adjusts a named reservation budget without allowing the
// new limit to fall below current usage.
func (e *Engine) SetResourceCapacity(ctx context.Context, name string, capacity int64) error {
	reply, err := e.sendControl(ctx, engineRequest{op: opSetResourceCapacity, resourceName: name, resourceCapacity: capacity})
	if err != nil {
		return err
	}
	return reply.err
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

// Cancel cancels an execution attempt by ID.
func (e *Engine) Cancel(id tasks.TaskID, reason tasks.Cause) (tasks.CancelReceipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.decisionTimeoutOrDefault())
	defer cancel()
	rep, err := e.sendControl(ctx, engineRequest{op: opCancel, taskID: id, reason: reason})
	if err != nil {
		return tasks.CancelReceipt{TaskID: id, Accepted: false, Reason: reason}, err
	}
	return rep.receipt, rep.err
}

// CancelScope cancels all active and queued tasks matching a scope and closes it.
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
