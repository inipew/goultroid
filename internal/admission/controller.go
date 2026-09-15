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
	PayloadBudget: 100 * 1024 * 1024,
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

	// Global owner state across all pools.
	ownerLimits       map[tasks.OwnerID]OwnerLimits
	ownerActiveCount  map[tasks.OwnerID]int
	ownerWaitingCount map[tasks.OwnerID]int
	ownerWaitingBytes map[tasks.OwnerID]int64

	// Ordering key locks: key -> active TaskID.
	orderingLocks map[string]tasks.TaskID
	taskByID      map[tasks.TaskID]*QueueEntry

	now func() time.Time
}

// NewController constructs an admission controller using the real clock.
func NewController(poolConfigs map[tasks.PoolID]PoolConfig) *Controller {
	return NewControllerWithClock(poolConfigs, time.Now)
}

// NewControllerWithClock constructs an admission controller with an injected
// wall clock. This keeps eligibility/deadline tests deterministic without
// changing production behavior.
func NewControllerWithClock(poolConfigs map[tasks.PoolID]PoolConfig, now func() time.Time) *Controller {
	if now == nil {
		now = time.Now
	}
	c := &Controller{
		pools:             make(map[tasks.PoolID]*poolState),
		ownerLimits:       make(map[tasks.OwnerID]OwnerLimits),
		ownerActiveCount:  make(map[tasks.OwnerID]int),
		ownerWaitingCount: make(map[tasks.OwnerID]int),
		ownerWaitingBytes: make(map[tasks.OwnerID]int64),
		orderingLocks:     make(map[string]tasks.TaskID),
		taskByID:          make(map[tasks.TaskID]*QueueEntry),
		now:               now,
	}
	for poolID, cfg := range poolConfigs {
		c.pools[poolID] = newPoolState(cfg)
	}
	return c
}

// SetOwnerLimits updates quotas for a specific owner. If that owner is already
// active in one or more DRR rings, its quantum is updated immediately rather
// than waiting for the ring to become empty and be rebuilt.
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
	for _, ps := range c.pools {
		for class := range ps.ownerQuantums {
			if _, ok := ps.ownerQuantums[class][owner]; ok {
				ps.ownerQuantums[class][owner] = limits.Weight
				// Do not retain credit from a previous, potentially much larger,
				// weight after a policy update.
				if ps.ownerDeficits[class][owner] > limits.Weight {
					ps.ownerDeficits[class][owner] = limits.Weight
				}
			}
		}
	}
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
	if ps.config.BacklogLimit > 0 && ps.totalWaiting >= ps.config.BacklogLimit {
		return tasks.NewAdmissionError(tasks.ReasonPoolBacklogFull, tasks.ErrPoolBacklogFull)
	}
	if ps.config.PayloadBudget > 0 && ps.totalBytes+payloadBytes > ps.config.PayloadBudget {
		return tasks.NewAdmissionError(tasks.ReasonPayloadBudget, tasks.ErrPayloadBudget)
	}
	limits := c.getOwnerLimits(spec.QuotaOwner)
	if limits.MaxWaiting > 0 && c.ownerWaitingCount[spec.QuotaOwner] >= limits.MaxWaiting {
		return tasks.NewAdmissionError(tasks.ReasonOwnerQueueFull, tasks.ErrOwnerQueueFull)
	}
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
	c.ensureActiveClass(ps, class)
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
	for _, o := range ps.activeOwners[class] {
		if o == owner {
			return
		}
	}
	ps.activeOwners[class] = append(ps.activeOwners[class], owner)
	limits := c.getOwnerLimits(owner)
	weight := limits.Weight
	if weight <= 0 {
		weight = 1
	}
	ps.ownerQuantums[class][owner] = weight
}

func removeOwnerAt(ps *poolState, class tasks.PriorityClass, index int) {
	owners := ps.activeOwners[class]
	if index < 0 || index >= len(owners) {
		return
	}
	copy(owners[index:], owners[index+1:])
	owners[len(owners)-1] = ""
	owners = owners[:len(owners)-1]
	ps.activeOwners[class] = owners
	cursor := ps.ownerCursor[class]
	if cursor > index {
		cursor--
	}
	if len(owners) == 0 || cursor >= len(owners) {
		cursor = 0
	}
	ps.ownerCursor[class] = cursor
}

func removeClassAt(ps *poolState, index int) {
	if index < 0 || index >= len(ps.activeClasses) {
		return
	}
	class := ps.activeClasses[index]
	copy(ps.activeClasses[index:], ps.activeClasses[index+1:])
	ps.activeClasses[len(ps.activeClasses)-1] = ""
	ps.activeClasses = ps.activeClasses[:len(ps.activeClasses)-1]
	delete(ps.classDeficits, class)
	cursor := ps.classCursor
	if cursor > index {
		cursor--
	}
	if len(ps.activeClasses) == 0 || cursor >= len(ps.activeClasses) {
		cursor = 0
	}
	ps.classCursor = cursor
}

func (c *Controller) pruneOwner(ps *poolState, class tasks.PriorityClass, owner tasks.OwnerID) {
	rq := ps.queues[class][owner]
	if rq != nil && rq.Len() != 0 {
		return
	}
	delete(ps.queues[class], owner)
	delete(ps.ownerDeficits[class], owner)
	delete(ps.ownerQuantums[class], owner)
	for i, active := range ps.activeOwners[class] {
		if active == owner {
			removeOwnerAt(ps, class, i)
			break
		}
	}
	if len(ps.activeOwners[class]) == 0 {
		for i, activeClass := range ps.activeClasses {
			if activeClass == class {
				removeClassAt(ps, i)
				break
			}
		}
	}
}

func (c *Controller) decrementWaiting(owner tasks.OwnerID, payload int64) {
	if n := c.ownerWaitingCount[owner]; n <= 1 {
		delete(c.ownerWaitingCount, owner)
	} else {
		c.ownerWaitingCount[owner] = n - 1
	}
	if b := c.ownerWaitingBytes[owner] - payload; b <= 0 {
		delete(c.ownerWaitingBytes, owner)
	} else {
		c.ownerWaitingBytes[owner] = b
	}
}

// SelectCandidate chooses the next eligible QueueEntry for dispatch using hierarchical DRR (ADR 0006 §6.2).
func (c *Controller) SelectCandidate(pool tasks.PoolID) (*QueueEntry, error) {
	ps, ok := c.pools[pool]
	if !ok || len(ps.activeClasses) == 0 {
		return nil, ErrNoEligibleTask
	}

	classCount := len(ps.activeClasses)
	for classTries := 0; classTries < classCount && len(ps.activeClasses) > 0; classTries++ {
		if ps.classCursor >= len(ps.activeClasses) {
			ps.classCursor = 0
		}
		classIndex := ps.classCursor
		class := ps.activeClasses[classIndex]
		quantum := ps.classQuantums[class]
		if quantum <= 0 {
			quantum = 1
		}
		if ps.classDeficits[class] <= 0 {
			ps.classDeficits[class] = quantum
		}
		owners := ps.activeOwners[class]
		if len(owners) == 0 {
			removeClassAt(ps, classIndex)
			continue
		}

		ownerCount := len(owners)
		cursor := ps.ownerCursor[class]
		for ownerTries := 0; ownerTries < ownerCount && len(owners) > 0; ownerTries++ {
			if cursor >= len(owners) {
				cursor = 0
			}
			owner := owners[cursor]
			ownerQuantum := ps.ownerQuantums[class][owner]
			if ownerQuantum <= 0 {
				ownerQuantum = 1
			}
			if ps.ownerDeficits[class][owner] <= 0 {
				ps.ownerDeficits[class][owner] = ownerQuantum
			}
			rq := ps.queues[class][owner]
			if rq == nil || rq.Len() == 0 {
				c.pruneOwner(ps, class, owner)
				owners = ps.activeOwners[class]
				if len(owners) == 0 {
					break
				}
				continue
			}
			head := rq.Peek()
			if head == nil {
				cursor++
				continue
			}
			now := c.now().UTC()
			if !head.Spec.QueueDeadline.IsZero() && !now.Before(head.Spec.QueueDeadline) {
				cursor++
				continue
			}
			limits := c.getOwnerLimits(owner)
			if limits.MaxActive > 0 && c.ownerActiveCount[owner] >= limits.MaxActive {
				cursor++
				continue
			}
			if head.Spec.OrderingKey != "" {
				if _, locked := c.orderingLocks[head.Spec.OrderingKey]; locked {
					cursor++
					continue
				}
			}

			ps.classDeficits[class]--
			ps.ownerDeficits[class][owner]--
			candidate := rq.Pop()
			ps.deadlineIndex.Remove(candidate)
			ps.totalWaiting--
			ps.totalBytes -= candidate.PayloadSize
			c.decrementWaiting(owner, candidate.PayloadSize)
			delete(c.taskByID, candidate.Spec.ID)
			c.ownerActiveCount[owner]++
			if candidate.Spec.OrderingKey != "" {
				c.orderingLocks[candidate.Spec.OrderingKey] = candidate.Spec.ID
			}

			if rq.Len() == 0 {
				c.pruneOwner(ps, class, owner)
			} else {
				ps.ownerCursor[class] = cursor
				if ps.ownerDeficits[class][owner] <= 0 {
					ps.ownerCursor[class]++
					ps.ownerDeficits[class][owner] = 0
				}
			}
			if ps.classDeficits[class] <= 0 && len(ps.activeClasses) > 0 {
				ps.classCursor++
			}
			return candidate, nil
		}

		if len(ps.activeOwners[class]) > 0 {
			ps.ownerCursor[class] = cursor
			ps.classDeficits[class] = 0
			ps.classCursor++
		}
	}
	return nil, ErrNoEligibleTask
}

// OnTaskTerminal is called when a physical execution attempt finishes, freeing owner active count and ordering key.
func (c *Controller) OnTaskTerminal(spec tasks.WorkSpec) {
	owner := spec.QuotaOwner
	if n := c.ownerActiveCount[owner]; n <= 1 {
		delete(c.ownerActiveCount, owner)
	} else {
		c.ownerActiveCount[owner] = n - 1
	}
	if spec.OrderingKey != "" && c.orderingLocks[spec.OrderingKey] == spec.ID {
		delete(c.orderingLocks, spec.OrderingKey)
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
	c.decrementWaiting(spec.QuotaOwner, entry.PayloadSize)
	delete(c.taskByID, id)
	c.pruneOwner(ps, class, spec.QuotaOwner)
	return entry, true
}

// PopExpired removes all expired entries. Runtime code should prefer
// PopExpiredN with an explicit turn budget.
func (c *Controller) PopExpired(pool tasks.PoolID, now time.Time) []*QueueEntry {
	return c.PopExpiredN(pool, now, 0)
}

// PopExpiredN removes up to limit expired ready entries and eagerly reclaims
// empty owner/class DRR state. This prevents high-cardinality owner churn from
// leaving zero-valued counters and empty rings resident indefinitely.
func (c *Controller) PopExpiredN(pool tasks.PoolID, now time.Time, limit int) []*QueueEntry {
	ps, ok := c.pools[pool]
	if !ok {
		return nil
	}
	expired := ps.deadlineIndex.PopExpiredN(now, limit)
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
		c.decrementWaiting(spec.QuotaOwner, entry.PayloadSize)
		delete(c.taskByID, spec.ID)
		c.pruneOwner(ps, class, spec.QuotaOwner)
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

// EarliestDeadline inspects the roots of all pool deadline min-heaps and returns the earliest queue deadline.
func (c *Controller) EarliestDeadline() (time.Time, bool) {
	var earliest time.Time
	hasEarliest := false
	for _, ps := range c.pools {
		if entry, ok := ps.deadlineIndex.PeekEarliest(); ok {
			if !hasEarliest || entry.Spec.QueueDeadline.Before(earliest) {
				earliest = entry.Spec.QueueDeadline
				hasEarliest = true
			}
		}
	}
	return earliest, hasEarliest
}
