package admission

import (
	"container/heap"
	"errors"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type deadlineEntry struct {
	taskID   tasks.TaskID
	deadline time.Time
	index    int
}

type deadlineHeap []*deadlineEntry

func (h deadlineHeap) Len() int { return len(h) }
func (h deadlineHeap) Less(i, j int) bool {
	if h[i].deadline.Equal(h[j].deadline) {
		return h[i].taskID < h[j].taskID
	}
	return h[i].deadline.Before(h[j].deadline)
}
func (h deadlineHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *deadlineHeap) Push(value any) {
	entry := value.(*deadlineEntry)
	entry.index = len(*h)
	*h = append(*h, entry)
}
func (h *deadlineHeap) Pop() any {
	old := *h
	last := len(old) - 1
	entry := old[last]
	old[last] = nil
	entry.index = -1
	*h = old[:last]
	return entry
}

// Deadlines is an indexed min-heap. Update and remove do not leave stale heap
// nodes behind, so repeated rescheduling/cancellation stays O(active tasks).
type Deadlines struct {
	heap   deadlineHeap
	byTask map[tasks.TaskID]*deadlineEntry
}

func NewDeadlines() *Deadlines {
	d := &Deadlines{byTask: make(map[tasks.TaskID]*deadlineEntry)}
	heap.Init(&d.heap)
	return d
}

func (d *Deadlines) Upsert(taskID tasks.TaskID, deadline time.Time) error {
	if taskID == "" || deadline.IsZero() {
		return errors.New("task ID and deadline are required")
	}
	if existing := d.byTask[taskID]; existing != nil {
		existing.deadline = deadline
		heap.Fix(&d.heap, existing.index)
		return nil
	}
	entry := &deadlineEntry{taskID: taskID, deadline: deadline, index: -1}
	d.byTask[taskID] = entry
	heap.Push(&d.heap, entry)
	return nil
}

func (d *Deadlines) Remove(taskID tasks.TaskID) (time.Time, bool) {
	entry := d.byTask[taskID]
	if entry == nil {
		return time.Time{}, false
	}
	delete(d.byTask, taskID)
	heap.Remove(&d.heap, entry.index)
	return entry.deadline, true
}

func (d *Deadlines) Peek() (tasks.TaskID, time.Time, bool) {
	if len(d.heap) == 0 {
		return "", time.Time{}, false
	}
	entry := d.heap[0]
	return entry.taskID, entry.deadline, true
}

func (d *Deadlines) PopDue(now time.Time) (tasks.TaskID, time.Time, bool) {
	if len(d.heap) == 0 || d.heap[0].deadline.After(now) {
		return "", time.Time{}, false
	}
	entry := heap.Pop(&d.heap).(*deadlineEntry)
	delete(d.byTask, entry.taskID)
	return entry.taskID, entry.deadline, true
}

func (d *Deadlines) Len() int { return len(d.heap) }
