package workers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/inipew/goultroid/internal/queue"
	"github.com/inipew/goultroid/internal/tasks"
)

// PoolStats reports real-time and cumulative metrics for a worker pool.
type PoolStats struct {
	Name          string      `json:"name"`
	Concurrency   int         `json:"concurrency"`
	Busy          int         `json:"busy"`
	QueueStats    queue.Stats `json:"queue"`
	TasksExecuted int64       `json:"tasks_executed"`
	TasksSuccess  int64       `json:"tasks_success"`
	TasksFailed   int64       `json:"tasks_failed"`
}

// Pool manages a dedicated set of worker goroutines fed by a bounded queue.
type Pool struct {
	name        string
	concurrency int
	queue       *queue.Queue[tasks.Task]

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	busy          atomic.Int32
	tasksExecuted atomic.Int64
	tasksSuccess  atomic.Int64
	tasksFailed   atomic.Int64

	startOnce sync.Once
	stopOnce  sync.Once
	stopDone  chan struct{}
	running   atomic.Bool
}

// NewPool creates a new named worker pool with specified concurrency and queue capacity.
func NewPool(name string, concurrency int, queueCapacity int, policy queue.OverflowPolicy) *Pool {
	if concurrency <= 0 {
		concurrency = 4
	}
	if queueCapacity <= 0 {
		queueCapacity = 100
	}

	return &Pool{
		name:        name,
		concurrency: concurrency,
		queue:       queue.New[tasks.Task](queueCapacity, policy),
		stopDone:    make(chan struct{}),
	}
}

// Name returns the name of this worker pool.
func (p *Pool) Name() string {
	return p.name
}

// Start spawns the worker goroutines for this pool.
func (p *Pool) Start(parentCtx context.Context) {
	p.startOnce.Do(func() {
		if parentCtx == nil {
			parentCtx = context.Background()
		}
		p.ctx, p.cancel = context.WithCancel(parentCtx)
		p.running.Store(true)

		for i := 0; i < p.concurrency; i++ {
			p.wg.Add(1)
			go p.workerLoop(i)
		}
	})
}

func (p *Pool) workerLoop(workerID int) {
	defer p.wg.Done()

	for {
		task, err := p.queue.Pop(p.ctx)
		if err != nil {
			// Pool cancelled or queue closed
			return
		}

		p.busy.Add(1)
		p.tasksExecuted.Add(1)

		taskErr := task.Execute(p.ctx)
		if taskErr == nil {
			p.tasksSuccess.Add(1)
		} else {
			p.tasksFailed.Add(1)
		}

		p.busy.Add(-1)
	}
}

// Submit queues a task for execution in this worker pool.
func (p *Pool) Submit(ctx context.Context, task tasks.Task) error {
	if !p.running.Load() {
		return errors.New("worker pool is not running")
	}

	task.State = tasks.StateQueued
	if err := p.queue.Push(ctx, task); err != nil {
		task.State = tasks.StateFailed
		task.Error = err
		return fmt.Errorf("pool %s submit rejected: %w", p.name, err)
	}
	return nil
}

// Stats returns a snapshot of pool concurrency and throughput.
func (p *Pool) Stats() PoolStats {
	return PoolStats{
		Name:          p.name,
		Concurrency:   p.concurrency,
		Busy:          int(p.busy.Load()),
		QueueStats:    p.queue.Stats(),
		TasksExecuted: p.tasksExecuted.Load(),
		TasksSuccess:  p.tasksSuccess.Load(),
		TasksFailed:   p.tasksFailed.Load(),
	}
}

// Stop gracefully drains or stops the worker pool.
func (p *Pool) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	p.stopOnce.Do(func() {
		p.running.Store(false)
		p.queue.Close()
		go func() {
			p.wg.Wait()
			close(p.stopDone)
		}()
	})

	select {
	case <-p.stopDone:
		if p.cancel != nil {
			p.cancel()
		}
		return nil
	case <-ctx.Done():
		if p.cancel != nil {
			p.cancel()
		}
		return fmt.Errorf("pool %s stop timed out: %w", p.name, ctx.Err())
	}
}
