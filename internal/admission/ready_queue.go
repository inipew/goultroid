package admission

import (
	"github.com/inipew/goultroid/internal/tasks"
)

type queueNode struct {
	entry *QueueEntry
	prev  *queueNode
	next  *queueNode
}

// ReadyQueue is an intrusive doubly linked FIFO ready queue with O(1) removal by TaskID (ADR 0006 §6.1).
type ReadyQueue struct {
	head  *queueNode
	tail  *queueNode
	nodes map[tasks.TaskID]*queueNode

	totalBytes int64
}

// NewReadyQueue creates an empty ready queue.
func NewReadyQueue() *ReadyQueue {
	return &ReadyQueue{
		nodes: make(map[tasks.TaskID]*queueNode),
	}
}

// Push appends an entry to the tail of the FIFO queue.
func (q *ReadyQueue) Push(entry *QueueEntry) {
	if entry == nil {
		return
	}
	node := &queueNode{entry: entry}
	q.nodes[entry.Spec.ID] = node
	q.totalBytes += entry.PayloadSize

	if q.tail == nil {
		q.head = node
		q.tail = node
	} else {
		q.tail.next = node
		node.prev = q.tail
		q.tail = node
	}
}

// Pop removes and returns the head entry of the queue.
func (q *ReadyQueue) Pop() *QueueEntry {
	if q.head == nil {
		return nil
	}
	node := q.head
	entry := node.entry
	q.removeNode(node)
	return entry
}

// Remove deletes an entry by TaskID in O(1).
func (q *ReadyQueue) Remove(id tasks.TaskID) *QueueEntry {
	node, ok := q.nodes[id]
	if !ok {
		return nil
	}
	entry := node.entry
	q.removeNode(node)
	return entry
}

func (q *ReadyQueue) removeNode(node *queueNode) {
	delete(q.nodes, node.entry.Spec.ID)
	q.totalBytes -= node.entry.PayloadSize

	if node.prev != nil {
		node.prev.next = node.next
	} else {
		q.head = node.next
	}

	if node.next != nil {
		node.next.prev = node.prev
	} else {
		q.tail = node.prev
	}

	node.entry = nil
	node.prev = nil
	node.next = nil
}

// Peek returns the head entry without removing it.
func (q *ReadyQueue) Peek() *QueueEntry {
	if q.head == nil {
		return nil
	}
	return q.head.entry
}

// Len returns the current count of items in the queue.
func (q *ReadyQueue) Len() int {
	return len(q.nodes)
}

// TotalBytes returns the sum of payload sizes in this queue.
func (q *ReadyQueue) TotalBytes() int64 {
	return q.totalBytes
}
