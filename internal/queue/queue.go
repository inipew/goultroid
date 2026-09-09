package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

var (
	ErrQueueFull   = errors.New("queue is full")
	ErrQueueClosed = errors.New("queue is closed")
)

// OverflowPolicy determines what happens when an item is pushed to a full queue.
type OverflowPolicy int

const (
	// PolicyReject returns ErrQueueFull immediately when the queue is at capacity.
	PolicyReject OverflowPolicy = iota
	// PolicyBlock blocks the caller until space becomes available or the context is cancelled.
	PolicyBlock
	// PolicyDrop silently drops the newly incoming item.
	PolicyDrop
	// PolicyDropOldest evicts the oldest item in the queue to make room for the new item.
	PolicyDropOldest
)

// Stats reports the cumulative metrics for the queue.
type Stats struct {
	Capacity int   `json:"capacity"`
	Depth    int   `json:"depth"`
	Enqueued int64 `json:"enqueued"`
	Dequeued int64 `json:"dequeued"`
	Dropped  int64 `json:"dropped"`
	Rejected int64 `json:"rejected"`
}

// Queue is a thread-safe, bounded FIFO queue supporting configurable overflow policies.
type Queue[T any] struct {
	mu       sync.Mutex
	items    []T
	capacity int
	policy   OverflowPolicy
	closed   bool

	notEmpty *sync.Cond
	notFull  *sync.Cond

	enqueued atomic.Int64
	dequeued atomic.Int64
	dropped  atomic.Int64
	rejected atomic.Int64
}

// New creates a new bounded Queue with the specified capacity and overflow policy.
func New[T any](capacity int, policy OverflowPolicy) *Queue[T] {
	if capacity <= 0 {
		capacity = 100
	}
	q := &Queue[T]{
		items:    make([]T, 0, capacity),
		capacity: capacity,
		policy:   policy,
	}
	q.notEmpty = sync.NewCond(&q.mu)
	q.notFull = sync.NewCond(&q.mu)
	return q
}

// Capacity returns the maximum buffer capacity of the queue.
func (q *Queue[T]) Capacity() int {
	return q.capacity
}

// Depth returns the current number of items buffered in the queue.
func (q *Queue[T]) Depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Stats returns a snapshot of queue statistics.
func (q *Queue[T]) Stats() Stats {
	q.mu.Lock()
	depth := len(q.items)
	q.mu.Unlock()

	return Stats{
		Capacity: q.capacity,
		Depth:    depth,
		Enqueued: q.enqueued.Load(),
		Dequeued: q.dequeued.Load(),
		Dropped:  q.dropped.Load(),
		Rejected: q.rejected.Load(),
	}
}

// Push adds an item to the queue in accordance with the configured overflow policy.
func (q *Queue[T]) Push(ctx context.Context, item T) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.items) >= q.capacity {
		if q.closed {
			return ErrQueueClosed
		}

		switch q.policy {
		case PolicyReject:
			q.rejected.Add(1)
			return ErrQueueFull

		case PolicyDrop:
			q.dropped.Add(1)
			return nil

		case PolicyDropOldest:
			if len(q.items) > 0 {
				q.items = q.items[1:]
				q.dropped.Add(1)
			}

		case PolicyBlock:
			if ctx == nil {
				ctx = context.Background()
			}
			if err := ctx.Err(); err != nil {
				return err
			}

			// Register one cancellation callback without keeping an extra goroutine
			// alive for every blocked producer.
			stopWake := context.AfterFunc(ctx, func() {
				q.mu.Lock()
				q.notFull.Broadcast()
				q.mu.Unlock()
			})
			q.notFull.Wait()
			stopWake()

			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}

	if q.closed {
		return ErrQueueClosed
	}

	q.items = append(q.items, item)
	q.enqueued.Add(1)
	q.notEmpty.Signal()
	return nil
}

// Pop retrieves and removes the oldest item from the queue, blocking until an item
// is available or the context is cancelled.
func (q *Queue[T]) Pop(ctx context.Context) (T, error) {
	var zero T
	q.mu.Lock()
	defer q.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	for len(q.items) == 0 {
		if q.closed {
			return zero, ErrQueueClosed
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}

		// Register one cancellation callback without keeping an extra goroutine
		// alive for every idle consumer.
		stopWake := context.AfterFunc(ctx, func() {
			q.mu.Lock()
			q.notEmpty.Broadcast()
			q.mu.Unlock()
		})
		q.notEmpty.Wait()
		stopWake()

		if err := ctx.Err(); err != nil {
			return zero, err
		}
	}

	if q.closed && len(q.items) == 0 {
		return zero, ErrQueueClosed
	}

	item := q.items[0]
	q.items = q.items[1:]
	q.dequeued.Add(1)
	q.notFull.Signal()
	return item, nil
}

// Close closes the queue, awakening any blocked callers.
func (q *Queue[T]) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		q.notEmpty.Broadcast()
		q.notFull.Broadcast()
	}
}
