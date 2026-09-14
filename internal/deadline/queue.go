package deadline

import (
	"container/heap"
	"errors"
	"time"
)

// Entry is one reference-only deadline. Payload/executable state deliberately
// stays with the subsystem that owns Key.
type Entry struct {
	Key      string
	Deadline time.Time
	Sequence uint64
}

type item struct {
	Entry
	index int
}

type minHeap []*item

func (h minHeap) Len() int { return len(h) }
func (h minHeap) Less(i, j int) bool {
	if h[i].Deadline.Equal(h[j].Deadline) {
		if h[i].Sequence == h[j].Sequence {
			return h[i].Key < h[j].Key
		}
		return h[i].Sequence < h[j].Sequence
	}
	return h[i].Deadline.Before(h[j].Deadline)
}
func (h minHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *minHeap) Push(v any) {
	n := v.(*item)
	n.index = len(*h)
	*h = append(*h, n)
}
func (h *minHeap) Pop() any {
	old := *h
	last := len(old) - 1
	n := old[last]
	old[last] = nil
	n.index = -1
	*h = old[:last]
	return n
}

// Queue is an indexed min-heap shared by execution control-plane subsystems.
// Upsert and Remove never leave stale heap nodes, keeping memory O(active
// deadlines) even when the same key is rescheduled repeatedly.
type Queue struct {
	heap minHeap
	byKey map[string]*item
	nextSequence uint64
}

func New() *Queue {
	q := &Queue{byKey: make(map[string]*item)}
	heap.Init(&q.heap)
	return q
}

// Upsert installs or replaces one deadline. Sequence zero asks Queue to assign
// a monotonic sequence; callers restoring durable timers may provide the saved
// non-zero sequence to preserve deterministic tie ordering across recovery.
func (q *Queue) Upsert(entry Entry) (Entry, error) {
	if entry.Key == "" || entry.Deadline.IsZero() {
		return Entry{}, errors.New("deadline key and time are required")
	}
	if current := q.byKey[entry.Key]; current != nil {
		if entry.Sequence == 0 {
			entry.Sequence = current.Sequence
		}
		if entry.Sequence > q.nextSequence {
			q.nextSequence = entry.Sequence
		}
		current.Entry = entry
		heap.Fix(&q.heap, current.index)
		return current.Entry, nil
	}
	if entry.Sequence == 0 {
		q.nextSequence++
		entry.Sequence = q.nextSequence
	} else if entry.Sequence > q.nextSequence {
		q.nextSequence = entry.Sequence
	}
	n := &item{Entry: entry, index: -1}
	q.byKey[entry.Key] = n
	heap.Push(&q.heap, n)
	return entry, nil
}

func (q *Queue) Remove(key string) (Entry, bool) {
	n := q.byKey[key]
	if n == nil {
		return Entry{}, false
	}
	delete(q.byKey, key)
	heap.Remove(&q.heap, n.index)
	return n.Entry, true
}

func (q *Queue) Peek() (Entry, bool) {
	if len(q.heap) == 0 {
		return Entry{}, false
	}
	return q.heap[0].Entry, true
}

func (q *Queue) PopDue(now time.Time) (Entry, bool) {
	if len(q.heap) == 0 || q.heap[0].Deadline.After(now) {
		return Entry{}, false
	}
	n := heap.Pop(&q.heap).(*item)
	delete(q.byKey, n.Key)
	return n.Entry, true
}

func (q *Queue) Len() int { return len(q.heap) }
