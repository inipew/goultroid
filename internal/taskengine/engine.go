package taskengine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/tasks"
)

var (
	ErrEngineNotRunning = errors.New("task engine is not running")
	ErrTaskNotFound     = errors.New("task was not found")
)

type slotPhase uint8

const (
	slotIdle slotPhase = iota + 1
	slotReserved
	slotAssigned
	slotRunning
)

type slotRecord struct {
	slot  tasks.WorkerSlot
	phase slotPhase
	task  tasks.TaskID
}

type ownerUsage struct {
	waiting      int
	waitingBytes int64
	active       int
}

type poolUsage struct {
	waiting      int
	waitingBytes int64
}

type taskRecord struct {
	spec          tasks.WorkSpec
	ticket        tasks.AdmissionTicket
	state         tasks.LifecycleState
	createdAt     time.Time
	admittedAt    time.Time
	queuedAt      time.Time
	dispatchingAt time.Time
	startedAt     time.Time
	finishedAt    time.Time

	queueDeadline time.Time
	cancelled     bool
	cancelReason  tasks.CancelReason

	permit    tasks.PhysicalPermit
	runCancel context.CancelFunc

	result     *tasks.TaskResult
	creditHeld bool
	retireAt   time.Time
}

// PoolStats is a coordinator-consistent view of one physical pool.
type PoolStats struct {
	Workers      int
	Idle         int
	Reserved     int
	Assigned     int
	Running      int
	Waiting      int
	WaitingBytes int64
}

// OwnerStats separates logical waiting from dispatch/running reservation.
type OwnerStats struct {
	Waiting      int
	WaitingBytes int64
	Active       int
}

// ResourceStats reports pre-execution resource reservation state.
type ResourceStats struct {
	Capacity uint32
	Used     uint32
}

// Stats is an immutable snapshot copied out of the single-writer coordinator.
type Stats struct {
	Accepting         bool
	Tasks             int
	ResultCreditsUsed int
	ResultCapacity    int
	Pools             map[tasks.PoolID]PoolStats
	Owners            map[tasks.QuotaOwner]OwnerStats
	Resources         map[string]ResourceStats
}

type submitRequest struct {
	ctx         context.Context
	spec        tasks.WorkSpec
	requestedAt time.Time
	response    chan submitResponse
}

type submitResponse struct {
	ticket tasks.AdmissionTicket
	err    error
}

type cancelRequest struct {
	taskID   tasks.TaskID
	reason   tasks.CancelReason
	response chan cancelResponse
}

type cancelResponse struct {
	receipt tasks.CancelReceipt
	err     error
}

type snapshotRequest struct {
	taskID   tasks.TaskID
	response chan snapshotResponse
}

type snapshotResponse struct {
	snapshot tasks.TaskSnapshot
	ok       bool
}

type consumeRequest struct {
	taskID   tasks.TaskID
	response chan consumeResponse
}

type consumeResponse struct {
	result tasks.TaskResult
	ok     bool
	err    error
}

type statsRequest struct{ response chan Stats }
type quiesceRequest struct{ response chan struct{} }
type drainRequest struct{ response chan (<-chan struct{}) }

type closeScopeRequest struct {
	scope    tasks.ScopeIdentity
	response chan int
}

type startedEvent struct {
	permit tasks.PhysicalPermit
	at     time.Time
}

type completedEvent struct {
	permit tasks.PhysicalPermit
	result tasks.TaskResult
}

// Engine is the single writer for mutable task state, logical admission,
// physical permit inventory, quotas, resource reservations, and result credits.
// It never executes feature handlers or performs storage/network I/O.
type Engine struct {
	cfg     Config
	catalog *Catalog
	workers tasks.PhysicalWorkers

	mu      sync.RWMutex
	started bool
	running bool
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}

	control       chan any
	startedEvents chan startedEvent
	results       chan completedEvent

	ready      *admission.Ready
	deadlines  *admission.Deadlines
	retentions *admission.Deadlines

	records      map[tasks.TaskID]*taskRecord
	closedScopes map[tasks.ScopeIdentity]struct{}
	owners       map[tasks.QuotaOwner]*ownerUsage
	pools        map[tasks.PoolID]*poolUsage
	resources    map[string]uint32
	ordering     map[string]tasks.TaskID

	poolOrder  []tasks.PoolID
	poolSlots  map[tasks.PoolID][]*slotRecord
	slotByID   map[tasks.WorkerID]*slotRecord
	slotCursor map[tasks.PoolID]int

	accepting         bool
	resultCreditsUsed int
	dispatchEpoch     uint64
	drainWaiters      []chan struct{}
}

func New(cfg Config, catalog *Catalog, workers tasks.PhysicalWorkers) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if catalog == nil {
		return nil, errors.New("task catalog is required")
	}
	if workers == nil {
		return nil, errors.New("physical workers are required")
	}
	ready, err := admission.NewReady(cfg.ClassQuantum, cfg.DefaultOwner.Weight)
	if err != nil {
		return nil, err
	}
	for owner, limits := range cfg.Owners {
		if err := ready.SetOwnerWeight(owner, limits.Weight); err != nil {
			return nil, err
		}
	}
	poolOrder := make([]tasks.PoolID, 0, len(cfg.Pools))
	for pool := range cfg.Pools {
		poolOrder = append(poolOrder, pool)
	}
	sort.Slice(poolOrder, func(i, j int) bool { return poolOrder[i] < poolOrder[j] })

	return &Engine{
		cfg:           cfg,
		catalog:       catalog,
		workers:       workers,
		control:       make(chan any, cfg.ControlInboxCapacity),
		startedEvents: make(chan startedEvent, totalWorkers(cfg)),
		results:       make(chan completedEvent, cfg.ResultCredits),
		ready:         ready,
		deadlines:     admission.NewDeadlines(),
		retentions:    admission.NewDeadlines(),
		records:       make(map[tasks.TaskID]*taskRecord),
		closedScopes:  make(map[tasks.ScopeIdentity]struct{}),
		owners:        make(map[tasks.QuotaOwner]*ownerUsage),
		pools:         make(map[tasks.PoolID]*poolUsage, len(cfg.Pools)),
		resources:     make(map[string]uint32, len(cfg.ResourceCapacity)),
		ordering:      make(map[string]tasks.TaskID),
		poolOrder:     poolOrder,
		poolSlots:     make(map[tasks.PoolID][]*slotRecord, len(cfg.Pools)),
		slotByID:      make(map[tasks.WorkerID]*slotRecord),
		slotCursor:    make(map[tasks.PoolID]int),
	}, nil
}

func totalWorkers(cfg Config) int {
	total := 0
	for _, limits := range cfg.Pools {
		total += limits.Workers
	}
	if total < 1 {
		return 1
	}
	return total
}

// Start snapshots real physical inventory. Worker generation zero is rejected
// because permits must be fenced across worker restarts.
func (e *Engine) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		if e.running {
			return nil
		}
		return errors.New("task engine cannot be restarted after stop")
	}

	counts := make(map[tasks.PoolID]int, len(e.cfg.Pools))
	poolSlots := make(map[tasks.PoolID][]*slotRecord, len(e.cfg.Pools))
	slotByID := make(map[tasks.WorkerID]*slotRecord)
	for _, slot := range e.workers.Slots() {
		if slot.Pool == "" || slot.WorkerID == "" || slot.Generation == 0 {
			return errors.New("physical worker inventory contains an invalid slot")
		}
		if _, configured := e.cfg.Pools[slot.Pool]; !configured {
			return fmt.Errorf("physical worker references unknown pool %q", slot.Pool)
		}
		if _, duplicate := slotByID[slot.WorkerID]; duplicate {
			return fmt.Errorf("duplicate physical worker ID %q", slot.WorkerID)
		}
		record := &slotRecord{slot: slot, phase: slotIdle}
		slotByID[slot.WorkerID] = record
		poolSlots[slot.Pool] = append(poolSlots[slot.Pool], record)
		counts[slot.Pool]++
	}
	for pool, limits := range e.cfg.Pools {
		if counts[pool] != limits.Workers {
			return fmt.Errorf("pool %q worker inventory = %d, config = %d", pool, counts[pool], limits.Workers)
		}
	}
	e.poolSlots = poolSlots
	e.slotByID = slotByID
	for pool := range e.cfg.Pools {
		e.pools[pool] = &poolUsage{}
	}

	e.ctx, e.cancel = context.WithCancel(ctx)
	e.done = make(chan struct{})
	e.accepting = true
	e.started = true
	e.running = true
	go e.loop()
	return nil
}

// Submit waits only for the coordinator's admission decision. Cancellation is
// linearized at inbox handoff: before handoff ctx can cancel the request; once
// handed off, Submit returns the coordinator's decision rather than returning a
// cancellation that could hide an accepted execution.
