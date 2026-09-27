package taskengine

import (
	"context"
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
	started         time.Time
	decision        *submitCell
	controlDecision *controlCell
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

// LaneRuntimeStats reports physical workers and outstanding work in a lazy lane.
type LaneRuntimeStats struct {
	WorkerLimit int
	Workers     int
	Pending     int
	Active      int
}

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
	DeliveryLane    LaneRuntimeStats
	DurabilityLane  LaneRuntimeStats
	ScopeTombstones int
	Pools           map[tasks.PoolID]PoolRuntimeStats
	Resources       map[string]ResourceRuntimeStats
}

// Engine coordinates admission, fairness, physical worker permits, result credits, and lifecycles.
// All mutable execution state below is owned exclusively by the runLoop goroutine.
type Engine struct {
	mu          sync.Mutex
	controlGate sync.RWMutex

	config    Config
	configErr error
	adm       *admission.Controller

	// ---- runLoop-owned execution state ----
	idleSlots         map[tasks.PoolID][]int
	poolConcurrencies map[tasks.PoolID]int
	poolMinWorkers    map[tasks.PoolID]int
	poolIdleTimeouts  map[tasks.PoolID]time.Duration
	workerRunning     map[tasks.PoolID][]bool
	workerIdleSince   map[tasks.PoolID][]time.Time
	workerCancels     map[tasks.PoolID][]context.CancelFunc
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
	// The inbox stores pooled pointers rather than engineRequest values. The
	// request union is intentionally broad and relatively large; pointer-backed
	// storage keeps the configured burst capacity without preallocating that
	// entire union for every empty channel slot.
	inbox       chan *engineRequest
	requestPool sync.Pool
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
