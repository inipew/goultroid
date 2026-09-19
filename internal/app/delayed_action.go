package app

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	defaultDelayedActionCapacity      = 4096
	defaultDelayedActionRetainedBytes = 4 << 20 // 4 MiB
	delayedActionOverheadBytes   int64 = 256
	delayedActionSubmitTimeout         = time.Second
	delayedActionExecutionTimeout      = 15 * time.Second
)

type delayedActionRequest struct {
	due           time.Time
	action        func(context.Context) error
	reply         chan error
	seq           uint64
	retainedBytes int64
}

type delayedActionItem struct {
	due           time.Time
	action        func(context.Context) error
	seq           uint64
	retainedBytes int64
}

type delayedActionHeap []*delayedActionItem

func (h delayedActionHeap) Len() int { return len(h) }
func (h delayedActionHeap) Less(i, j int) bool {
	if h[i].due.Equal(h[j].due) {
		return h[i].seq < h[j].seq
	}
	return h[i].due.Before(h[j].due)
}
func (h delayedActionHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *delayedActionHeap) Push(x any)    { *h = append(*h, x.(*delayedActionItem)) }
func (h *delayedActionHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return item
}

// delayedActionScheduler owns only timing. Once an action becomes due, its I/O
// is submitted to TaskEngine as maintenance work, so the timer loop never sleeps
// a physical worker and never performs Telegram I/O itself.
type delayedActionScheduler struct {
	tasks tasks.Client

	mu        sync.Mutex
	runCtx    context.Context
	cancel    context.CancelFunc
	requests  chan delayedActionRequest
	done      chan struct{}
	accepting bool

	admissions       int
	coordinatorCount int
	coordinatorIdle  chan struct{}

	seq             atomic.Uint64
	pending         atomic.Int64
	pendingBytes    atomic.Int64
	submitFailures  atomic.Int64
	byteRejections  atomic.Uint64
	maxPending      int
	maxRetainedBytes int64
}

func newDelayedActionScheduler(client tasks.Client) *delayedActionScheduler {
	return &delayedActionScheduler{
		tasks:            client,
		maxPending:       defaultDelayedActionCapacity,
		maxRetainedBytes: defaultDelayedActionRetainedBytes,
	}
}

func (s *delayedActionScheduler) Name() string { return "delayed-actions" }
func (s *delayedActionScheduler) Dependencies() []string { return []string{"taskengine"} }

func (s *delayedActionScheduler) Start(ctx context.Context) error {
	if s == nil || s.tasks == nil {
		return errors.New("delayed action scheduler requires task client")
	}
	if ctx == nil {
		return errors.New("delayed action scheduler start context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accepting {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.runCtx = runCtx
	s.cancel = cancel
	s.requests = nil
	s.done = nil
	s.accepting = true
	return nil
}

func (s *delayedActionScheduler) ensureCoordinatorLocked() (chan delayedActionRequest, chan struct{}, error) {
	if !s.accepting || s.runCtx == nil || s.runCtx.Err() != nil {
		return nil, nil, fmt.Errorf("%w: delayed action scheduler is not accepting work", core.ErrUnavailable)
	}
	if s.requests != nil && s.done != nil {
		return s.requests, s.done, nil
	}

	requests := make(chan delayedActionRequest, 256)
	done := make(chan struct{})
	s.requests = requests
	s.done = done
	if s.coordinatorCount == 0 {
		s.coordinatorIdle = make(chan struct{})
	}
	s.coordinatorCount++
	runCtx := s.runCtx
	go s.run(runCtx, requests, done)
	return requests, done, nil
}

func (s *delayedActionScheduler) finishAdmission() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.admissions > 0 {
		s.admissions--
	}
	s.mu.Unlock()
}

func (s *delayedActionScheduler) tryRetireCoordinator(requests chan delayedActionRequest, done chan struct{}) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepting || s.requests != requests || s.done != done {
		return false
	}
	if s.admissions != 0 || len(requests) != 0 {
		return false
	}
	// Mark this coordinator unavailable before it exits. A concurrent Schedule
	// will create a new coordinator instead of enqueueing onto an orphaned
	// buffered channel.
	s.requests = nil
	s.done = nil
	return true
}

func (s *delayedActionScheduler) releaseReservation(retainedBytes int64) {
	if s == nil || retainedBytes <= 0 {
		return
	}
	s.mu.Lock()
	if s.pending.Load() > 0 {
		s.pending.Add(-1)
	}
	remaining := s.pendingBytes.Add(-retainedBytes)
	if remaining < 0 {
		s.pendingBytes.Store(0)
	}
	s.mu.Unlock()
}

func (s *delayedActionScheduler) Schedule(ctx context.Context, delay time.Duration, retainedBytes int64, action func(context.Context) error) error {
	if s == nil || action == nil {
		return fmt.Errorf("%w: delayed action scheduler is unavailable", core.ErrUnavailable)
	}
	if ctx == nil {
		return errors.New("delayed action admission context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay < 0 {
		delay = 0
	}
	if retainedBytes < 0 {
		return fmt.Errorf("%w: delayed action retained bytes cannot be negative", core.ErrValidation)
	}

	s.mu.Lock()
	if !s.accepting || s.runCtx == nil || s.runCtx.Err() != nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: delayed action scheduler is not accepting work", core.ErrUnavailable)
	}
	if s.pending.Load() >= int64(s.maxPending) {
		s.mu.Unlock()
		return fmt.Errorf("%w: delayed action capacity %d reached", core.ErrResourceLimit, s.maxPending)
	}
	if s.maxRetainedBytes > 0 {
		remaining := s.maxRetainedBytes - s.pendingBytes.Load()
		if remaining < delayedActionOverheadBytes || retainedBytes > remaining-delayedActionOverheadBytes {
			s.byteRejections.Add(1)
			s.mu.Unlock()
			return fmt.Errorf("%w: delayed action retained-byte budget reached", core.ErrResourceLimit)
		}
	}
	charge := retainedBytes + delayedActionOverheadBytes
	requests, done, err := s.ensureCoordinatorLocked()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.pending.Add(1)
	s.pendingBytes.Add(charge)
	s.admissions++
	s.mu.Unlock()

	admissionActive := true
	finishAdmission := func() {
		if !admissionActive {
			return
		}
		admissionActive = false
		s.finishAdmission()
	}

	reply := make(chan error, 1)
	req := delayedActionRequest{
		due:           time.Now().Add(delay),
		action:        action,
		reply:         reply,
		seq:           s.seq.Add(1),
		retainedBytes: charge,
	}
	owned := false
	defer func() {
		finishAdmission()
		if !owned {
			s.releaseReservation(charge)
		}
	}()

	select {
	case requests <- req:
		owned = true
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return fmt.Errorf("%w: delayed action scheduler stopped", core.ErrUnavailable)
	}

	select {
	case err := <-reply:
		finishAdmission()
		return err
	case <-ctx.Done():
		finishAdmission()
		return ctx.Err()
	case <-done:
		finishAdmission()
		return fmt.Errorf("%w: delayed action scheduler stopped", core.ErrUnavailable)
	}
}

func (s *delayedActionScheduler) Quiesce(context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.accepting = false
	cancel := s.cancel
	if s.coordinatorCount == 0 {
		s.runCtx = nil
		s.cancel = nil
		s.requests = nil
		s.done = nil
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (s *delayedActionScheduler) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("delayed action scheduler stop context is nil")
	}
	_ = s.Quiesce(ctx)

	s.mu.Lock()
	idle := s.coordinatorIdle
	count := s.coordinatorCount
	s.mu.Unlock()
	if count == 0 || idle == nil {
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *delayedActionScheduler) Health(context.Context) runtime.ComponentHealth {
	if s == nil {
		return runtime.ComponentHealth{Status: runtime.HealthUnhealthy, Details: "scheduler is nil"}
	}
	failures := s.submitFailures.Load()
	if failures > 0 {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: fmt.Sprintf("task submission failures: %d", failures)}
	}
	s.mu.Lock()
	coordinators := s.coordinatorCount
	s.mu.Unlock()
	return runtime.ComponentHealth{Status: runtime.HealthHealthy, Details: fmt.Sprintf(
		"pending=%d retained_bytes=%d/%d byte_rejections=%d coordinators=%d",
		s.pending.Load(), s.pendingBytes.Load(), s.maxRetainedBytes, s.byteRejections.Load(), coordinators,
	)}
}

func (s *delayedActionScheduler) run(ctx context.Context, requests chan delayedActionRequest, done chan struct{}) {
	defer close(done)
	var queue delayedActionHeap
	heap.Init(&queue)
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}

		terminated := ctx.Err() != nil
		if terminated {
			// Component shutdown owns every queued/admitted reservation. Active
			// Schedule callers are released by done closing below.
			s.pending.Store(0)
			s.pendingBytes.Store(0)
		}

		s.mu.Lock()
		if s.requests == requests && s.done == done {
			s.requests = nil
			s.done = nil
		}
		if terminated && s.runCtx == ctx {
			s.accepting = false
			s.cancel = nil
			s.runCtx = nil
		}
		if s.coordinatorCount > 0 {
			s.coordinatorCount--
		}
		if s.coordinatorCount == 0 && s.coordinatorIdle != nil {
			close(s.coordinatorIdle)
			s.coordinatorIdle = nil
		}
		s.mu.Unlock()
	}()

	for {
		if queue.Len() == 0 && s.tryRetireCoordinator(requests, done) {
			return
		}
		var timerC <-chan time.Time
		if queue.Len() > 0 {
			wait := time.Until(queue[0].due)
			if wait < 0 {
				wait = 0
			}
			if timer == nil {
				timer = time.NewTimer(wait)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(wait)
			}
			timerC = timer.C
		}

		select {
		case <-ctx.Done():
			return
		case req := <-requests:
			heap.Push(&queue, &delayedActionItem{
				due: req.due, action: req.action, seq: req.seq, retainedBytes: req.retainedBytes,
			})
			req.reply <- nil
		case <-timerC:
			now := time.Now()
			for queue.Len() > 0 && !queue[0].due.After(now) {
				item := heap.Pop(&queue).(*delayedActionItem)
				s.releaseReservation(item.retainedBytes)
				s.submit(ctx, item)
			}
		}
	}
}

func (s *delayedActionScheduler) submit(ctx context.Context, item *delayedActionItem) {
	if item == nil || item.action == nil || ctx.Err() != nil {
		return
	}
	submitCtx, cancel := context.WithTimeout(ctx, delayedActionSubmitTimeout)
	defer cancel()
	now := time.Now()
	_, err := s.tasks.Submit(submitCtx, tasks.WorkSpec{
		ID:               tasks.TaskID(fmt.Sprintf("delayed-action:%d", item.seq)),
		QuotaOwner:       tasks.OwnerID("system:delayed-actions"),
		Pool:             "general",
		Class:            tasks.PriorityMaintenance,
		OrderingKey:      fmt.Sprintf("delayed-action:%d", item.seq),
		QueueDeadline:    now.Add(5 * time.Second),
		ExecutionTimeout: delayedActionExecutionTimeout,
		Handler:          item.action,
	})
	if err != nil {
		s.submitFailures.Add(1)
	}
}
