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
	defaultDelayedActionCapacity = 4096
	delayedActionSubmitTimeout    = time.Second
	delayedActionExecutionTimeout = 15 * time.Second
)

type delayedActionRequest struct {
	due    time.Time
	action func(context.Context) error
	reply  chan error
	seq    uint64
}

type delayedActionItem struct {
	due    time.Time
	action func(context.Context) error
	seq    uint64
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

	seq            atomic.Uint64
	pending        atomic.Int64
	submitFailures atomic.Int64
	maxPending     int
}

func newDelayedActionScheduler(client tasks.Client) *delayedActionScheduler {
	return &delayedActionScheduler{tasks: client, maxPending: defaultDelayedActionCapacity}
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
	s.requests = make(chan delayedActionRequest, 256)
	s.done = make(chan struct{})
	s.accepting = true
	go s.run(runCtx, s.requests, s.done)
	return nil
}

func (s *delayedActionScheduler) Schedule(ctx context.Context, delay time.Duration, action func(context.Context) error) error {
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

	s.mu.Lock()
	if !s.accepting || s.requests == nil || s.done == nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: delayed action scheduler is not accepting work", core.ErrUnavailable)
	}
	requests := s.requests
	done := s.done
	s.mu.Unlock()

	reply := make(chan error, 1)
	req := delayedActionRequest{
		due:    time.Now().Add(delay),
		action: action,
		reply:  reply,
		seq:    s.seq.Add(1),
	}
	select {
	case requests <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return fmt.Errorf("%w: delayed action scheduler stopped", core.ErrUnavailable)
	}

	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
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
	done := s.done
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
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
	return runtime.ComponentHealth{Status: runtime.HealthHealthy, Details: fmt.Sprintf("pending=%d", s.pending.Load())}
}

func (s *delayedActionScheduler) run(ctx context.Context, requests <-chan delayedActionRequest, done chan struct{}) {
	defer close(done)
	var queue delayedActionHeap
	heap.Init(&queue)
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
		s.pending.Store(0)
		s.mu.Lock()
		if s.done == done {
			s.accepting = false
			s.cancel = nil
			s.runCtx = nil
		}
		s.mu.Unlock()
	}()

	for {
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
			if queue.Len() >= s.maxPending {
				req.reply <- fmt.Errorf("%w: delayed action capacity %d reached", core.ErrResourceLimit, s.maxPending)
				continue
			}
			heap.Push(&queue, &delayedActionItem{due: req.due, action: req.action, seq: req.seq})
			s.pending.Store(int64(queue.Len()))
			req.reply <- nil
		case <-timerC:
			now := time.Now()
			for queue.Len() > 0 && !queue[0].due.After(now) {
				item := heap.Pop(&queue).(*delayedActionItem)
				s.pending.Store(int64(queue.Len()))
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
