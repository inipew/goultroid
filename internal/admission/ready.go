package admission

import (
	"container/list"
	"errors"
	"fmt"

	"github.com/inipew/goultroid/internal/tasks"
)

// Item is the minimal immutable scheduling metadata kept by the pure admission
// data structure. TaskEngine remains the owner of full task state.
type Item struct {
	TaskID       tasks.TaskID
	Pool         tasks.PoolID
	Class        tasks.PriorityClass
	Owner        tasks.QuotaOwner
	PayloadBytes int64
}

type indexedItem struct {
	item  Item
	owner *ownerQueue
	elem  *list.Element
}

type ownerQueue struct {
	owner   tasks.QuotaOwner
	weight  int
	deficit int
	items   list.List
	index   int // -1 while quota-blocked/inactive
}

type classQueue struct {
	class   tasks.PriorityClass
	quantum int
	deficit int
	owners  []*ownerQueue // active/eligible owners only
	byOwner map[tasks.QuotaOwner]*ownerQueue
	cursor  int
}

type poolQueue struct {
	classes []*classQueue
	byClass map[tasks.PriorityClass]*classQueue
	cursor  int
}

// Ready implements hierarchical deficit round-robin: class DRR followed by
// quota-owner DRR, with FIFO order inside each owner queue. Owners blocked by a
// global MaxActive quota are removed from the active rings until explicitly
// unblocked, so dispatch does not repeatedly scan known-ineligible owners.
type Ready struct {
	pools         map[tasks.PoolID]*poolQueue
	items         map[tasks.TaskID]*indexedItem
	classQuantum  map[tasks.PriorityClass]int
	ownerWeight   map[tasks.QuotaOwner]int
	blockedOwners map[tasks.QuotaOwner]struct{}
	defaultWeight int
}

func NewReady(classQuantum map[tasks.PriorityClass]int, defaultOwnerWeight int) (*Ready, error) {
	if defaultOwnerWeight <= 0 {
		return nil, errors.New("default owner weight must be positive")
	}
	copyQuantum := make(map[tasks.PriorityClass]int, 4)
	for class := tasks.PriorityInteractive; class <= tasks.PriorityMaintenance; class++ {
		q := classQuantum[class]
		if q <= 0 {
			return nil, fmt.Errorf("priority class %d requires positive quantum", class)
		}
		copyQuantum[class] = q
	}
	return &Ready{
		pools:         make(map[tasks.PoolID]*poolQueue),
		items:         make(map[tasks.TaskID]*indexedItem),
		classQuantum:  copyQuantum,
		ownerWeight:   make(map[tasks.QuotaOwner]int),
		blockedOwners: make(map[tasks.QuotaOwner]struct{}),
		defaultWeight: defaultOwnerWeight,
	}, nil
}

func (r *Ready) SetOwnerWeight(owner tasks.QuotaOwner, weight int) error {
	if owner == "" || weight <= 0 {
		return errors.New("owner and positive weight are required")
	}
	r.ownerWeight[owner] = weight
	for _, pool := range r.pools {
		for _, class := range pool.classes {
			if oq := class.byOwner[owner]; oq != nil {
				oq.weight = weight
				if oq.deficit > weight*4 {
					oq.deficit = weight * 4
				}
			}
		}
	}
	return nil
}

// BlockOwner removes every queue for owner from active DRR rings while keeping
// its FIFO items indexed for exact cancellation/deadline removal.
func (r *Ready) BlockOwner(owner tasks.QuotaOwner) {
	if owner == "" {
		return
	}
	if _, blocked := r.blockedOwners[owner]; blocked {
		return
	}
	r.blockedOwners[owner] = struct{}{}
	for _, pool := range r.pools {
		for _, class := range pool.classes {
			if oq := class.byOwner[owner]; oq != nil && oq.index >= 0 {
				r.deactivateOwner(class, oq)
			}
		}
	}
}

// UnblockOwner reactivates non-empty queues for owner after global capacity is
// returned. Old deficit is reset on block/deactivation to avoid credit bursts.
func (r *Ready) UnblockOwner(owner tasks.QuotaOwner) {
	if _, blocked := r.blockedOwners[owner]; !blocked {
		return
	}
	delete(r.blockedOwners, owner)
	for _, pool := range r.pools {
		for _, class := range pool.classes {
			if oq := class.byOwner[owner]; oq != nil && oq.items.Len() > 0 && oq.index < 0 {
				r.activateOwner(class, oq)
			}
		}
	}
}

func (r *Ready) OwnerBlocked(owner tasks.QuotaOwner) bool {
	_, blocked := r.blockedOwners[owner]
	return blocked
}

func (r *Ready) Enqueue(item Item) error {
	if item.TaskID == "" || item.Pool == "" || item.Owner == "" || !item.Class.Valid() || item.PayloadBytes < 0 {
		return errors.New("invalid ready item")
	}
	if _, exists := r.items[item.TaskID]; exists {
		return fmt.Errorf("task already queued: %s", item.TaskID)
	}
	pool := r.ensurePool(item.Pool)
	class := pool.byClass[item.Class]
	owner := class.byOwner[item.Owner]
	if owner == nil {
		weight := r.ownerWeight[item.Owner]
		if weight <= 0 {
			weight = r.defaultWeight
		}
		owner = &ownerQueue{owner: item.Owner, weight: weight, index: -1}
		class.byOwner[item.Owner] = owner
		if _, blocked := r.blockedOwners[item.Owner]; !blocked {
			r.activateOwner(class, owner)
		}
	}
	elem := owner.items.PushBack(item)
	r.items[item.TaskID] = &indexedItem{item: item, owner: owner, elem: elem}
	return nil
}

func (r *Ready) Remove(taskID tasks.TaskID) (Item, bool) {
	indexed := r.items[taskID]
	if indexed == nil {
		return Item{}, false
	}
	item := indexed.item
	indexed.owner.items.Remove(indexed.elem)
	delete(r.items, taskID)
	if indexed.owner.items.Len() == 0 {
		pool := r.pools[item.Pool]
		class := pool.byClass[item.Class]
		r.removeOwner(class, indexed.owner)
	}
	return item, true
}

func (r *Ready) Len() int { return len(r.items) }

func (r *Ready) LenPool(pool tasks.PoolID) int {
	pq := r.pools[pool]
	if pq == nil {
		return 0
	}
	total := 0
	for _, class := range pq.classes {
		for _, owner := range class.byOwner {
			total += owner.items.Len()
		}
	}
	return total
}

// Next removes and returns the next eligible item. eligibility is evaluated
// only against owner FIFO heads; blocked heads preserve per-owner ordering.
func (r *Ready) Next(poolID tasks.PoolID, eligible func(Item) bool) (Item, bool) {
	pool := r.pools[poolID]
	if pool == nil || len(pool.classes) == 0 {
		return Item{}, false
	}
	activeOwners := 0
	for _, class := range pool.classes {
		activeOwners += len(class.owners)
	}
	if activeOwners == 0 {
		return Item{}, false
	}

	// Two full class/owner rounds are enough to grant fresh quantum and inspect
	// every active owner without unbounded skipping of resource-blocked heads.
	maxVisits := activeOwners*2 + len(pool.classes)*2
	for visits := 0; visits < maxVisits; visits++ {
		if pool.cursor >= len(pool.classes) {
			pool.cursor = 0
		}
		class := pool.classes[pool.cursor]
		if len(class.owners) == 0 {
			class.deficit = 0
			pool.cursor = (pool.cursor + 1) % len(pool.classes)
			continue
		}
		if class.deficit <= 0 {
			class.deficit += class.quantum
			if class.deficit > class.quantum*4 {
				class.deficit = class.quantum * 4
			}
		}
		if class.cursor >= len(class.owners) {
			class.cursor = 0
		}
		owner := class.owners[class.cursor]
		if owner.deficit <= 0 {
			owner.deficit += owner.weight
			if owner.deficit > owner.weight*4 {
				owner.deficit = owner.weight * 4
			}
		}
		front := owner.items.Front()
		if front == nil {
			r.removeOwner(class, owner)
			continue
		}
		item := front.Value.(Item)
		if class.deficit > 0 && owner.deficit > 0 && (eligible == nil || eligible(item)) {
			owner.items.Remove(front)
			delete(r.items, item.TaskID)
			class.deficit--
			owner.deficit--
			if owner.items.Len() == 0 {
				r.removeOwner(class, owner)
			} else if owner.deficit <= 0 {
				class.cursor = (class.cursor + 1) % len(class.owners)
			}
			if class.deficit <= 0 {
				pool.cursor = (pool.cursor + 1) % len(pool.classes)
			}
			return item, true
		}
		class.cursor = (class.cursor + 1) % len(class.owners)
		if class.cursor == 0 {
			pool.cursor = (pool.cursor + 1) % len(pool.classes)
		}
	}
	return Item{}, false
}

func (r *Ready) ensurePool(id tasks.PoolID) *poolQueue {
	if existing := r.pools[id]; existing != nil {
		return existing
	}
	pool := &poolQueue{byClass: make(map[tasks.PriorityClass]*classQueue)}
	for class := tasks.PriorityInteractive; class <= tasks.PriorityMaintenance; class++ {
		cq := &classQueue{class: class, quantum: r.classQuantum[class], byOwner: make(map[tasks.QuotaOwner]*ownerQueue)}
		pool.classes = append(pool.classes, cq)
		pool.byClass[class] = cq
	}
	r.pools[id] = pool
	return pool
}

func (r *Ready) activateOwner(class *classQueue, owner *ownerQueue) {
	if owner.index >= 0 {
		return
	}
	owner.index = len(class.owners)
	owner.deficit = 0
	class.owners = append(class.owners, owner)
}

func (r *Ready) deactivateOwner(class *classQueue, owner *ownerQueue) {
	idx := owner.index
	last := len(class.owners) - 1
	if idx < 0 || idx > last {
		owner.index = -1
		owner.deficit = 0
		return
	}
	if idx != last {
		moved := class.owners[last]
		class.owners[idx] = moved
		moved.index = idx
	}
	class.owners = class.owners[:last]
	owner.index = -1
	owner.deficit = 0
	if len(class.owners) == 0 {
		class.cursor = 0
		class.deficit = 0
		return
	}
	if class.cursor > idx {
		class.cursor--
	}
	if class.cursor >= len(class.owners) {
		class.cursor = 0
	}
}

func (r *Ready) removeOwner(class *classQueue, owner *ownerQueue) {
	if owner.index >= 0 {
		r.deactivateOwner(class, owner)
	}
	delete(class.byOwner, owner.owner)
	owner.index = -1
	owner.deficit = 0
}
