package scheduler

import (
	"container/heap"
	"time"
)

// TimerKind classifies the deadline purpose in the scheduler heap (ADR 0006 §8).
type TimerKind string

const (
	TimerScheduleOccurrence TimerKind = "schedule_occurrence"
	TimerRetryReady         TimerKind = "retry_ready"
	TimerMaintenance        TimerKind = "maintenance"
	TimerLeaseExpiry        TimerKind = "lease_expiry"
)

// TimerEntry represents an indexed deadline entry in the scheduler min-heap.
// It stores references and versions, NOT execution closures (ADR 0006 §8).
type TimerEntry struct {
	Kind       TimerKind
	Owner      string
	ID         string
	Generation uint64
	Deadline   time.Time
	Sequence   uint64
	HeapIndex  int
	Data       any
}

type timerHeap []*TimerEntry

func (h timerHeap) Len() int { return len(h) }

func (h timerHeap) Less(i, j int) bool {
	if h[i].Deadline.Equal(h[j].Deadline) {
		return h[i].Sequence < h[j].Sequence
	}
	return h[i].Deadline.Before(h[j].Deadline)
}

func (h timerHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].HeapIndex = i
	h[j].HeapIndex = j
}

func (h *timerHeap) Push(x any) {
	n := len(*h)
	item := x.(*TimerEntry)
	item.HeapIndex = n
	*h = append(*h, item)
}

func (h *timerHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.HeapIndex = -1
	*h = old[0 : n-1]
	return item
}

// IndexedHeap provides an indexed min-heap for timing deadlines with O(log N) operations.
type IndexedHeap struct {
	h   timerHeap
	seq uint64
}

// NewIndexedHeap creates an empty indexed deadline heap.
func NewIndexedHeap() *IndexedHeap {
	return &IndexedHeap{
		h: make(timerHeap, 0),
	}
}

// Push adds or updates a timer entry in the heap.
func (ih *IndexedHeap) Push(entry *TimerEntry) {
	if entry == nil || entry.Deadline.IsZero() {
		return
	}
	ih.seq++
	entry.Sequence = ih.seq
	heap.Push(&ih.h, entry)
}

// Remove removes an entry from the heap in O(log N).
func (ih *IndexedHeap) Remove(entry *TimerEntry) {
	if entry == nil || entry.HeapIndex < 0 || entry.HeapIndex >= len(ih.h) {
		return
	}
	heap.Remove(&ih.h, entry.HeapIndex)
	entry.HeapIndex = -1
}

// PeekEarliest returns the earliest entry in O(1).
func (ih *IndexedHeap) PeekEarliest() (*TimerEntry, bool) {
	if len(ih.h) == 0 {
		return nil, false
	}
	return ih.h[0], true
}

// PopDue removes and returns up to maxBatch entries whose deadlines are before or equal to now.
func (ih *IndexedHeap) PopDue(now time.Time, maxBatch int) []*TimerEntry {
	if maxBatch <= 0 {
		maxBatch = 100
	}
	var due []*TimerEntry
	for len(ih.h) > 0 && len(due) < maxBatch {
		top := ih.h[0]
		if top.Deadline.After(now) {
			break
		}
		item := heap.Pop(&ih.h).(*TimerEntry)
		item.HeapIndex = -1
		due = append(due, item)
	}
	return due
}

// Len returns the current count of timer entries in the heap.
func (ih *IndexedHeap) Len() int {
	return len(ih.h)
}
