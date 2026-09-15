package admission

import (
	"errors"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

var (
	ErrPoolNotConfigured = errors.New("worker pool not configured in admission controller")
	ErrNoEligibleTask    = errors.New("no eligible task available for dispatch")
)

// PoolConfig holds admission limits and capacity parameters for a single pool.
type PoolConfig struct {
	BacklogLimit  int   `json:"backlog_limit"`
	PayloadBudget int64 `json:"payload_budget"`
}

// DefaultPoolConfig provides standard pool admission parameters.
var DefaultPoolConfig = PoolConfig{
	BacklogLimit:  500,
	PayloadBudget: 100 * 1024 * 1024, // 100 MB
}

type poolState struct {
	config        PoolConfig
	deadlineIndex *DeadlineIndex

	// Queues: class -> owner -> ReadyQueue
	queues map[tasks.PriorityClass]map[tasks.OwnerID]*ReadyQueue

	// DRR class tracking
	classQuantums map[tasks.PriorityClass]int
	classDeficits map[tasks.PriorityClass]int
	activeClasses []tasks.PriorityClass
	classCursor   int

	// DRR owner tracking per class: class -> owner -> deficit
	ownerQuantums map[tasks.PriorityClass]map[tasks.OwnerID]int
	ownerDeficits map[tasks.PriorityClass]map[tasks.OwnerID]int
	activeOwners  map[tasks.PriorityClass][]tasks.OwnerID
	ownerCursor   map[tasks.PriorityClass]int

	totalWaiting int
	totalBytes   int64
}

func newPoolState(cfg PoolConfig) *poolState {
	ps := &poolState{
		config:        cfg,
		deadlineIndex: NewDeadlineIndex(),
		queues:        make(map[tasks.PriorityClass]map[tasks.OwnerID]*ReadyQueue),
		classQuantums: map[tasks.PriorityClass]int{
			tasks.PriorityInteractive: 8,
			tasks.PriorityNormal:      4,
			tasks.PriorityBackground:  2,
			tasks.PriorityMaintenance: 1,
		},
		classDeficits: make(map[tasks.PriorityClass]int),
		ownerQuantums: make(map[tasks.PriorityClass]map[tasks.OwnerID]int),
		ownerDeficits: make(map[tasks.PriorityClass]map[tasks.OwnerID]int),
		activeOwners:  make(map[tasks.PriorityClass][]tasks.OwnerID),
		ownerCursor:   make(map[tasks.PriorityClass]int),
	}
	for _, class := range []tasks.PriorityClass{
		tasks.PriorityInteractive,
		tasks.PriorityNormal,
		tasks.PriorityBackground,
		tasks.PriorityMaintenance,
	} {
		ps.queues[class] = make(map[tasks.OwnerID]*ReadyQueue)
		ps.ownerQuantums[class] = make(map[tasks.OwnerID]int)
		ps.ownerDeficits[class] = make(map[tasks.OwnerID]int)
	}
	return ps
}

// Controller implements Hierarchical Deficit Round Robin (DRR), quota checking,
// queue deadline eviction, and ordering key serialization (ADR 0006 §6).
type Controller struct {
	pools map[tasks.PoolID]*poolState

	// Global owner state across all pools
	ownerLimits       map[tasks.OwnerID]OwnerLimits
	ownerActiveCount  map[tasks.OwnerID]int
	ownerWaitingCount map[tasks.OwnerID]int
	ownerWaitingBytes map[tasks.OwnerID]int64

	// Ordering key locks: key -> active TaskID
	orderingLocks map[string]tasks.TaskID
	taskByID      map[tasks.TaskID]*QueueEntry
}

// NewController constructs an admission controller initialized with standard pool configs.
func NewController(poolConfigs map[tasks.PoolID]PoolConfig) *Controller {
	c := &Controller{
		pools:             make(map[tasks.PoolID]*poolState),
		ownerLimits:       make(map[tasks.OwnerID]OwnerLimits),
		ownerActiveCount:  make(map[tasks.OwnerID]int),
		ownerWaitingCount: make(map[tasks.OwnerID]int),
		ownerWaitingBytes: make(map[tasks.OwnerID]int64),
		orderingLocks:     make(map[string]tasks.TaskID),
		taskByID:          make(map[tasks.TaskID]*QueueEntry),
	}
	for poolID, cfg := range poolConfigs {
		c.pools[poolID] = newPoolState(cfg)
	}
	return c
}

// SetOwnerLimits updates quotas for a specific owner.
func (c *Controller) SetOwnerLimits(owner tasks.OwnerID, limits OwnerLimits) {
	if limits.MaxWaiting <= 0 {
		limits.MaxWaiting = DefaultOwnerLimits.MaxWaiting
	}
	if limits.MaxActive <= 0 {
		limits.MaxActive = DefaultOwnerLimits.MaxActive
	}
	if limits.Weight <= 0 {
		limits.Weight = DefaultOwnerLimits.Weight
	}
	if limits.MaxPayloadByte <= 0 {
		limits.MaxPayloadByte = DefaultOwnerLimits.MaxPayloadByte
	}
	c.ownerLimits[owner] = limits
}

func (c *Controller) getOwnerLimits(owner tasks.OwnerID) OwnerLimits {
	if limits, ok := c.ownerLimits[owner]; ok {
		return limits
	}
	return DefaultOwnerLimits
}

// CanAdmit tests whether a work specification can be accepted under capacity and owner quotas.
func (c *Controller) CanAdmit(spec tasks.WorkSpec, payloadBytes int64) error {
	ps, ok := c.pools[spec.Pool]
	if !ok {
		return tasks.NewAdmissionError(tasks.ReasonPoolBacklogFull, ErrPoolNotConfigured)
	}

	// 1. Pool backlog limit
	if ps.config.BacklogLimit > 0 && ps.totalWaiting >= ps.config.BacklogLimit {
		return tasks.NewAdmissionError(tasks.ReasonPoolBacklogFull, tasks.ErrPoolBacklogFull)
	}

	// 2. Pool payload budget
	if ps.config.PayloadBudget > 0 && ps.totalBytes+payloadBytes > ps.config.PayloadBudget {
		return tasks.NewAdmissionError(tasks.ReasonPayloadBudget, tasks.ErrPayloadBudget)
	}

	limits := c.getOwnerLimits(spec.QuotaOwner)

	// 3. Owner max waiting
	if limits.MaxWaiting > 0 && c.ownerWaitingCount[spec.QuotaOwner] >= limits.MaxWaiting {
		return tasks.NewAdmissionError(tasks.ReasonOwnerQueueFull, tasks.ErrOwnerQueueFull)
	}

	// 4. Owner max payload
	if limits.MaxPayloadByte > 0 && c.ownerWaitingBytes[spec.QuotaOwner]+payloadBytes > limits.MaxPayloadByte {
		return tasks.NewAdmissionError(tasks.ReasonPayloadBudget, tasks.ErrPayloadBudget)
	}

	return nil
}

// Enqueue adds an admitted work specification into the appropriate DRR ready queue.
func (c *Controller) Enqueue(entry *QueueEntry) {
	if entry == nil {
		return
	}
	spec := entry.Spec
	ps := c.pools[spec.Pool]
	if ps == nil {
		return
	}

	class := spec.Class
	if class == "" {
		class = tasks.PriorityNormal
	}

	owner := spec.QuotaOwner
	q, ok := ps.queues[class][owner]
	if !ok {
		q = NewReadyQueue()
		ps.queues[class][owner] = q
	}

	q.Push(entry)
	ps.deadlineIndex.Push(entry)
	ps.totalWaiting++
	ps.totalBytes += entry.PayloadSize

	c.ownerWaitingCount[owner]++
	c.ownerWaitingBytes[owner] += entry.PayloadSize
	c.taskByID[spec.ID] = entry

	// Maintain DRR active classes
	c.ensureActiveClass(ps, class)
	// Maintain DRR active owners for class
	c.ensureActiveOwner(ps, class, owner)
}

func (c *Controller) ensureActiveClass(ps *poolState, class tasks.PriorityClass) {
	for _, cl := range ps.activeClasses {
		if cl == class {
			return
		}
	}
	ps.activeClasses = append(ps.activeClasses, class)
}

func (c *Controller) ensureActiveOwner(ps *poolState, class tasks.PriorityClass, owner tasks.OwnerID) {
	owners := ps.activeOwners[class]
	for _, o := range owners {
		if o == owner {
			return
		}
	}
	ps.activeOwners[class] = append(ps.activeOwners[class], owner)
	if _, ok := ps.ownerQuantums[class][owner]; !ok {
		limits := c.getOwnerLimits(owner)
		weight := limits.Weight
		if weight <= 0 {
			weight = 1
		}
		ps.ownerQuantums[class][owner] = weight
	}
}

// SelectCandidate chooses the next eligible QueueEntry for dispatch using hierarchical DRR (ADR 0006 §6.2).
// Returns ErrNoEligibleTask if no candidates are currently eligible (e.g. owners at MaxActive or ordering blocked).
func (c *Controller) SelectCandidate(pool tasks.PoolID) (*QueueEntry, error) {
	ps, ok := c.pools[pool]
	if !ok || len(ps.activeClasses) == 0 {
		return nil, ErrNoEligibleTask
	}

	classCount := len(ps.activeClasses)
	for classTries := 0; classTries < classCount; classTries++ {
		if ps.classCursor >= len(ps.activeClasses) {
			ps.classCursor = 0
		}
		class := ps.activeClasses[ps.classCursor]
		quantum := ps.classQuantums[class]
		if quantum <= 0 {
			quantum = 1
		}
		ps.classDeficits[class] += quantum

		// Traverse active owners for this class
		owners := ps.activeOwners[class]
		if len(owners) == 0 {
			// Remove empty class from active with slot zeroing
			n := len(ps.activeClasses)
			copy(ps.activeClasses[ps.classCursor:], ps.activeClasses[ps.classCursor+1:])
			ps.activeClasses[n-1] = ""
			ps.activeClasses = ps.activeClasses[:n-1]
			continue
		}

		ownerCount := len(owners)
		cursor := ps.ownerCursor[class]
		for ownerTries := 0; ownerTries < ownerCount; ownerTries++ {
			if cursor >= len(owners) {
				cursor = 0
			}
			owner := owners[cursor]
			ownerQuantum := ps.ownerQuantums[class][owner]
			if ownerQuantum <= 0 {
				ownerQuantum = 1
			}
			ps.ownerDeficits[class][owner] += ownerQuantum

			rq := ps.queues[class][owner]
			if rq == nil || rq.Len() == 0 {
				// Remove empty owner from active with slot zeroing
				n := len(owners)
				copy(owners[cursor:], owners[cursor+1:])
				owners[n-1] = ""
				owners = owners[:n-1]
				ps.activeOwners[class] = owners
				continue
			}

			head := rq.Peek()
			if head == nil {
				cursor++
				continue
			}

			// Validate QueueDeadline (strict pre-dispatch check, ADR 0006 §5.2)
			now := time.Now().UTC()
			if !head.Spec.QueueDeadline.IsZero() && now.After(head.Spec.QueueDeadline) {
				cursor++
				continue
			}

			// Check owner MaxActive constraint
			limits := c.getOwnerLimits(owner)
			if limits.MaxActive > 0 && c.ownerActiveCount[owner] >= limits.MaxActive {
				// Owner blocked by concurrency; skip to next owner
				cursor++
				continue
			}

			// Check ordering key constraint
			if head.Spec.OrderingKey != "" {
				if _, locked := c.orderingLocks[head.Spec.OrderingKey]; locked {
					// Ordering key currently in-flight; skip
					cursor++
					continue
				}
			}

			// Found eligible candidate!
			// Debit cost = 1 from class deficit and owner deficit
			ps.classDeficits[class]--
			ps.ownerDeficits[class][owner]--

			// Pop from ReadyQueue and DeadlineIndex
			candidate := rq.Pop()
			ps.deadlineIndex.Remove(candidate)
			ps.totalWaiting--
			ps.totalBytes -= candidate.PayloadSize

			c.ownerWaitingCount[owner]--
			c.ownerWaitingBytes[owner] -= candidate.PayloadSize
			delete(c.taskByID, candidate.Spec.ID)

			// Record active dispatch
			c.ownerActiveCount[owner]++
			if candidate.Spec.OrderingKey != "" {
				c.orderingLocks[candidate.Spec.OrderingKey] = candidate.Spec.ID
			}

			ps.ownerCursor[class] = cursor + 1
			return candidate, nil
		}

		ps.ownerCursor[class] = cursor
		ps.classCursor++
	}

	return nil, ErrNoEligibleTask
}

// OnTaskTerminal is called when a physical execution attempt finishes, freeing owner active count and ordering key.
func (c *Controller) OnTaskTerminal(spec tasks.WorkSpec) {
	owner := spec.QuotaOwner
	if c.ownerActiveCount[owner] > 0 {
		c.ownerActiveCount[owner]--
	}
	if spec.OrderingKey != "" {
		if c.orderingLocks[spec.OrderingKey] == spec.ID {
			delete(c.orderingLocks, spec.OrderingKey)
		}
	}
}

// RemoveTask cancels a task from the waiting queue if it has not yet been dispatched.
func (c *Controller) RemoveTask(id tasks.TaskID) (*QueueEntry, bool) {
	entry, ok := c.taskByID[id]
	if !ok {
		return nil, false
	}
	spec := entry.Spec
	ps := c.pools[spec.Pool]
	if ps == nil {
		return nil, false
	}

	class := spec.Class
	if class == "" {
		class = tasks.PriorityNormal
	}

	rq := ps.queues[class][spec.QuotaOwner]
	if rq != nil {
		rq.Remove(id)
	}
	ps.deadlineIndex.Remove(entry)
	ps.totalWaiting--
	ps.totalBytes -= entry.PayloadSize

	c.ownerWaitingCount[spec.QuotaOwner]--
	c.ownerWaitingBytes[spec.QuotaOwner] -= entry.PayloadSize
	delete(c.taskByID, id)

	return entry, true
}

// PopExpired removes and returns all ready entries whose queue deadline has expired.
func (c *Controller) PopExpired(pool tasks.PoolID, now time.Time) []*QueueEntry {
	ps, ok := c.pools[pool]
	if !ok {
		return nil
	}

	expired := ps.deadlineIndex.PopExpired(now)
	for _, entry := range expired {
		spec := entry.Spec
		class := spec.Class
		if class == "" {
			class = tasks.PriorityNormal
		}
		rq := ps.queues[class][spec.QuotaOwner]
		if rq != nil {
			rq.Remove(spec.ID)
		}
		ps.totalWaiting--
		ps.totalBytes -= entry.PayloadSize

		c.ownerWaitingCount[spec.QuotaOwner]--
		c.ownerWaitingBytes[spec.QuotaOwner] -= entry.PayloadSize
		delete(c.taskByID, spec.ID)
	}
	return expired
}

// WaitingCount returns the number of queued tasks waiting for admission in a pool.
func (c *Controller) WaitingCount(pool tasks.PoolID) int {
	if ps, ok := c.pools[pool]; ok {
		return ps.totalWaiting
	}
	return 0
}
