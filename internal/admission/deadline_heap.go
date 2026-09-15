package admission

import (
	"container/heap"
	"time"
)

type deadlineHeap []*QueueEntry

func (h deadlineHeap) Len() int { return len(h) }
func (h deadlineHeap) Less(i, j int) bool {
	return h[i].Spec.QueueDeadline.Before(h[j].Spec.QueueDeadline)
}
func (h deadlineHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].HeapIndex = i
	h[j].HeapIndex = j
}

func (h *deadlineHeap) Push(x any) {
	n := len(*h)
	item := x.(*QueueEntry)
	item.HeapIndex = n
	*h = append(*h, item)
}

func (h *deadlineHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.HeapIndex = -1
	*h = old[0 : n-1]
	return item
}

// DeadlineIndex maintains an indexed min-heap of queue entries by their deadline (ADR 0006 §6.1).
type DeadlineIndex struct {
	h deadlineHeap
}

// NewDeadlineIndex constructs a new empty deadline index.
func NewDeadlineIndex() *DeadlineIndex {
	return &DeadlineIndex{h: make(deadlineHeap, 0)}
}

// Push inserts an entry with a non-zero deadline into the min-heap.
func (d *DeadlineIndex) Push(entry *QueueEntry) {
	if entry == nil || entry.Spec.QueueDeadline.IsZero() {
		if entry != nil {
			entry.HeapIndex = -1
		}
		return
	}
	if entry.HeapIndex >= 0 && entry.HeapIndex < len(d.h) && d.h[entry.HeapIndex] == entry {
		heap.Fix(&d.h, entry.HeapIndex)
		return
	}
	heap.Push(&d.h, entry)
}

// Remove removes an entry from the min-heap in O(log N).
func (d *DeadlineIndex) Remove(entry *QueueEntry) {
	if entry == nil || entry.HeapIndex < 0 || entry.HeapIndex >= len(d.h) || d.h[entry.HeapIndex] != entry {
		return
	}
	heap.Remove(&d.h, entry.HeapIndex)
	entry.HeapIndex = -1
}

// PeekEarliest inspects the root of the min-heap in O(1).
func (d *DeadlineIndex) PeekEarliest() (*QueueEntry, bool) {
	if len(d.h) == 0 {
		return nil, false
	}
	return d.h[0], true
}

// PopExpired removes and returns all entries whose deadlines are before or equal to now.
func (d *DeadlineIndex) PopExpired(now time.Time) []*QueueEntry {
	var expired []*QueueEntry
	for len(d.h) > 0 {
		top := d.h[0]
		if top.Spec.QueueDeadline.After(now) {
			break
		}
		item := heap.Pop(&d.h).(*QueueEntry)
		item.HeapIndex = -1
		expired = append(expired, item)
	}
	return expired
}

// Len returns the count of indexed deadline entries.
func (d *DeadlineIndex) Len() int {
	return len(d.h)
}
