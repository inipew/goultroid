package workers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/queue"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	PoolGeneral      = "general"
	PoolDownload     = "download"
	PoolMediaProcess = "media-process"
	PoolScheduler    = "scheduler"
)

// Ensure Manager implements runtime.Component.
var _ runtime.Component = (*Manager)(nil)

type admissionRequest struct {
	tm          *tasks.Manager
	taskCtx     context.Context
	cancel      context.CancelFunc
	task        tasks.Task
	reservation *ExecutionReservation
}

// Manager coordinates isolated worker pools across the application runtime.
type Manager struct {
	mu           sync.RWMutex
	pools        map[string]*Pool
	tasksManager *tasks.Manager
	accepting    bool
	acceptingEnd chan struct{}
	admissionCtx context.Context
	admissionEnd context.CancelFunc
	// admissionQueues are bounded one-per-pool producer queues. A fixed
	// controller goroutine drains each queue; accepted tasks never allocate a
	// dedicated admission waiter goroutine.
	admissionQueues map[string]chan admissionRequest
	admissionWG     sync.WaitGroup
	drainMu         sync.Mutex
	drained         bool
}

// NewManager creates a Manager initialized with standard workload pools.
func NewManager() *Manager {
	m := &Manager{pools: make(map[string]*Pool)}

	// Initialize default isolated pools per Blueprint §30
	m.pools[PoolGeneral] = NewPool(PoolGeneral, 8, 200, queue.PolicyBlock)
	m.pools[PoolInteractive] = NewPool(PoolInteractive, 32, 128, queue.PolicyReject)
	m.pools[PoolDownload] = NewPool(PoolDownload, 3, 50, queue.PolicyReject)
	m.pools[PoolMediaProcess] = NewPool(PoolMediaProcess, 2, 20, queue.PolicyReject)
	m.pools[PoolScheduler] = NewPool(PoolScheduler, 4, 100, queue.PolicyBlock)

	return m
}

func (m *Manager) Name() string { return "workers" }

func (m *Manager) Dependencies() []string { return nil }

// Start starts all managed worker pools and their fixed admission controllers.
func (m *Manager) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	m.admissionCtx, m.admissionEnd = context.WithCancel(ctx)
	m.accepting = true
	m.acceptingEnd = make(chan struct{})
	m.admissionQueues = make(map[string]chan admissionRequest, len(m.pools))
	m.drained = false
	pools := make(map[string]*Pool, len(m.pools))
	for name, pool := range m.pools {
		pools[name] = pool
		m.admissionQueues[name] = make(chan admissionRequest, cap(pool.admissions))
	}
	admissionCtx := m.admissionCtx
	acceptingEnd := m.acceptingEnd
	m.mu.Unlock()

	for _, pool := range pools {
		pool.Start(ctx)
	}
	for name, pool := range pools {
		m.mu.RLock()
		input := m.admissionQueues[name]
		m.mu.RUnlock()
		m.admissionWG.Add(1)
		go m.admissionLoop(admissionCtx, acceptingEnd, pool, input)
	}
	return nil
}

// Stop gracefully stops all managed worker pools.
func (m *Manager) Stop(ctx context.Context) error {
	if err := m.Quiesce(ctx); err != nil {
		return err
	}
	return m.Drain(ctx)
}

// Quiesce closes task admission without interrupting already accepted work.
func (m *Manager) Quiesce(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.accepting {
		m.accepting = false
		close(m.acceptingEnd)
	}
	return nil
}

// Drain waits for logical admission and then drains every physical pool.
func (m *Manager) Drain(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.drainMu.Lock()
	defer m.drainMu.Unlock()
	if m.drained {
		return nil
	}
	m.mu.Lock()
	pools := make([]*Pool, 0, len(m.pools))
	for _, pool := range m.pools {
		pools = append(pools, pool)
	}
	admissionEnd := m.admissionEnd
	m.mu.Unlock()
	if admissionEnd != nil {
		defer admissionEnd()
	}

	admissionsDone := make(chan struct{})
	go func() {
		m.admissionWG.Wait()
		close(admissionsDone)
	}()
	select {
	case <-admissionsDone:
	case <-ctx.Done():
		if admissionEnd != nil {
			admissionEnd()
		}
		return ctx.Err()
	}

	var stopErrs []error
	for _, pool := range pools {
		if err := pool.Stop(ctx); err != nil {
			stopErrs = append(stopErrs, err)
		}
	}
	if len(stopErrs) > 0 {
		return fmt.Errorf("worker manager stop encountered errors: %w", errors.Join(stopErrs...))
	}
	m.drained = true
	return nil
}

// Health probes the health status of all worker pools.
func (m *Manager) Health(ctx context.Context) runtime.ComponentHealth {
	stats := m.AllStats()
	for _, s := range stats {
		if s.QueueStats.Capacity > 0 && s.QueueStats.Depth >= s.QueueStats.Capacity {
			return runtime.ComponentHealth{
				Status:  runtime.HealthDegraded,
				Details: fmt.Sprintf("pool %s queue is saturated (%d/%d)", s.Name, s.QueueStats.Depth, s.QueueStats.Capacity),
			}
		}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}

// AddPool registers a custom worker pool.
func (m *Manager) AddPool(pool *Pool) error {
	if pool == nil {
		return fmt.Errorf("cannot add nil worker pool")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.pools[pool.Name()]; exists {
		return fmt.Errorf("worker pool %q already exists", pool.Name())
	}
	m.pools[pool.Name()] = pool
	return nil
}

// Get returns the named pool if found.
func (m *Manager) Get(name string) (*Pool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.pools[name]
	return p, ok
}

// SetTasksManager attaches the task manager for quota enforcement and lifecycle tracking.
func (m *Manager) SetTasksManager(tm *tasks.Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasksManager = tm
}

// TasksManager returns the configured task manager if attached.
func (m *Manager) TasksManager() *tasks.Manager {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tasksManager
}

// SetOwnerQuota sets task execution and queue quota for a specific owner.
func (m *Manager) SetOwnerQuota(owner string, q tasks.Quota) {
	m.mu.RLock()
	tm := m.tasksManager
	m.mu.RUnlock()
	if tm != nil {
		tm.SetOwnerQuota(owner, q)
	}
}

// GetOwnerQuota returns the quota for a specific owner.
func (m *Manager) GetOwnerQuota(owner string) (tasks.Quota, bool) {
	m.mu.RLock()
	tm := m.tasksManager
	m.mu.RUnlock()
	if tm == nil {
		return tasks.DefaultQuota, false
	}
	return tm.GetQuota(owner), true
}

// OwnerTaskStats returns execution metrics for a specific task owner.
func (m *Manager) OwnerTaskStats(owner string) (tasks.OwnerStats, bool) {
	m.mu.RLock()
	tm := m.tasksManager
	m.mu.RUnlock()
	if tm == nil {
		return tasks.OwnerStats{}, false
	}
	stats := tm.Stats()
	if os, ok := stats.Owners[owner]; ok {
		return os, true
	}
	return tasks.OwnerStats{Owner: owner}, true
}

// Submit dispatches a task to the designated worker pool with quota and lifecycle management.
func (m *Manager) Submit(ctx context.Context, poolName string, task tasks.Task) error {
	return m.submit(ctx, poolName, task, nil, false)
}

// TrySubmit performs the same lifecycle handoff as Submit but never waits for
// logical admission capacity. It is intended for nested/orchestration paths
// where blocking a worker on the same target pool could create starvation.
func (m *Manager) TrySubmit(ctx context.Context, poolName string, task tasks.Task) error {
	return m.submit(ctx, poolName, task, nil, true)
}

// SubmitReserved submits work backed by a physical execution reservation.
// WorkerManager owns the reservation after this call: it is released when the
// task physically starts, or on any admission/cancellation failure before then.
func (m *Manager) SubmitReserved(ctx context.Context, poolName string, task tasks.Task, reservation *ExecutionReservation) error {
	if reservation == nil {
		return errors.New("execution reservation is nil")
	}
	return m.submit(ctx, poolName, task, reservation, false)
}

func (m *Manager) submit(ctx context.Context, poolName string, task tasks.Task, reservation *ExecutionReservation, nonBlockingAdmission bool) error {
	reservationAccepted := false
	defer func() {
		if reservation != nil && !reservationAccepted {
			reservation.Release()
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.RLock()
	pool, ok := m.pools[poolName]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("worker pool %q not found", poolName)
	}
	tm := m.tasksManager
	accepting := m.accepting
	acceptingEnd := m.acceptingEnd
	m.mu.RUnlock()
	if !accepting {
		return fmt.Errorf("worker manager is not accepting tasks")
	}

	if tm == nil {
		if reservation != nil {
			return errors.New("execution reservation requires TaskManager lifecycle tracking")
		}
		if nonBlockingAdmission {
			return errors.New("non-blocking admission requires TaskManager lifecycle tracking")
		}
		return pool.Submit(ctx, task)
	}

	var admissionErr error
	if nonBlockingAdmission {
		admissionErr = pool.tryReserveAdmission()
	} else {
		admissionErr = pool.reserveAdmission(ctx, acceptingEnd)
	}
	if admissionErr != nil {
		return fmt.Errorf("pool %s admission rejected: %w", poolName, admissionErr)
	}

	// Hold the manager lock across registration and the buffered controller
	// handoff. Quiesce therefore cannot close ingress between "accepted" and
	// enqueueing the admission request.
	m.mu.Lock()
	if !m.accepting || m.acceptingEnd != acceptingEnd {
		m.mu.Unlock()
		pool.releaseAdmission()
		return fmt.Errorf("worker manager is not accepting tasks")
	}
	admissionQueue := m.admissionQueues[poolName]
	if admissionQueue == nil {
		m.mu.Unlock()
		pool.releaseAdmission()
		return fmt.Errorf("pool %s admission controller is not running", poolName)
	}
	if task.ID == "" {
		task.ID = fmt.Sprintf("task:%s:%d", poolName, time.Now().UnixNano())
	}
	taskCtx, cancel, err := tm.Register(ctx, task)
	if err != nil {
		m.mu.Unlock()
		pool.releaseAdmission()
		return err
	}

	origRun := task.Run
	task.Run = func(runCtx context.Context) error {
		startCtx, startErr := tm.MarkRunning(task.ID)
		if reservation != nil {
			reservation.Release()
		}
		if startErr != nil {
			cancel()
			state := tasks.StateFailed
			if errors.Is(startErr, context.Canceled) {
				state = tasks.StateCancelled
			} else if errors.Is(startErr, context.DeadlineExceeded) {
				state = tasks.StateTimedOut
			}
			tm.Finish(task.ID, state, startErr)
			return startErr
		}
		defer cancel()

		execCtx, execCancel := context.WithCancel(startCtx)
		stopPoolCancel := context.AfterFunc(runCtx, execCancel)
		defer stopPoolCancel()
		defer execCancel()

		var runErr error
		if err := execCtx.Err(); err != nil {
			runErr = err
		} else if origRun != nil {
			runErr = origRun(execCtx)
		}
		// Preserve the physical execution deadline instead of collapsing it into
		// context.Canceled through the merged task context.
		if errors.Is(runErr, context.Canceled) && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			runErr = context.DeadlineExceeded
		}

		state := tasks.StateCompleted
		if runErr != nil {
			switch {
			case errors.Is(runErr, context.DeadlineExceeded):
				state = tasks.StateTimedOut
			case errors.Is(runErr, context.Canceled):
				state = tasks.StateCancelled
			default:
				state = tasks.StateFailed
			}
		}
		tm.Finish(task.ID, state, runErr)
		return runErr
	}

	request := admissionRequest{
		tm: tm, taskCtx: taskCtx, cancel: cancel, task: task, reservation: reservation,
	}
	// Every request owns one pool admission token and this channel has exactly
	// that bounded capacity, so the handoff cannot exceed the configured limit.
	admissionQueue <- request
	reservationAccepted = true
	m.mu.Unlock()
	return nil
}

func (m *Manager) admissionLoop(ctx context.Context, acceptingEnd <-chan struct{}, pool *Pool, input <-chan admissionRequest) {
	defer m.admissionWG.Done()
	pending := make([]admissionRequest, 0, cap(pool.admissions))
	quiescing := false

	removePending := func(i int) {
		copy(pending[i:], pending[i+1:])
		pending[len(pending)-1] = admissionRequest{}
		pending = pending[:len(pending)-1]
	}

	for {
		// First absorb every request already handed off so one owner blocked by
		// quota cannot prevent later owners from being considered.
		for {
			select {
			case req := <-input:
				pending = append(pending, req)
			default:
				goto dispatch
			}
		}

	dispatch:
		progressed := false
		for i := 0; i < len(pending); {
			req := pending[i]
			if err := req.taskCtx.Err(); err != nil {
				m.failAdmission(pool, req, err)
				removePending(i)
				progressed = true
				continue
			}

			queueCtx, err := req.tm.TryQueue(req.task.ID)
			if errors.Is(err, tasks.ErrQuotaExceeded) {
				i++
				continue
			}
			if err != nil {
				m.failAdmission(pool, req, err)
				removePending(i)
				progressed = true
				continue
			}

			enqueueCtx, enqueueCancel := context.WithCancel(queueCtx)
			stopAdmission := context.AfterFunc(ctx, enqueueCancel)
			err = pool.submitAccepted(enqueueCtx, req.task)
			stopAdmission()
			enqueueCancel()
			pool.releaseAdmission()
			if err != nil {
				if req.reservation != nil {
					req.reservation.Release()
				}
				req.cancel()
				state := tasks.StateFailed
				if errors.Is(err, context.Canceled) {
					state = tasks.StateCancelled
				} else if errors.Is(err, context.DeadlineExceeded) {
					state = tasks.StateTimedOut
				}
				req.tm.Finish(req.task.ID, state, err)
			}
			removePending(i)
			progressed = true
		}

		if quiescing && len(pending) == 0 && len(input) == 0 {
			return
		}
		if progressed {
			continue
		}

		var slotChanged <-chan struct{}
		m.mu.RLock()
		tm := m.tasksManager
		m.mu.RUnlock()
		if tm != nil {
			slotChanged = tm.SlotChanges()
		}
		select {
		case req := <-input:
			pending = append(pending, req)
		case <-slotChanged:
		case <-acceptingEnd:
			quiescing = true
			acceptingEnd = nil
		case <-ctx.Done():
			for _, req := range pending {
				m.failAdmission(pool, req, ctx.Err())
			}
			for {
				select {
				case req := <-input:
					m.failAdmission(pool, req, ctx.Err())
				default:
					return
				}
			}
		}
	}
}

func (m *Manager) failAdmission(pool *Pool, req admissionRequest, err error) {
	pool.releaseAdmission()
	if req.reservation != nil {
		req.reservation.Release()
	}
	req.cancel()
	state := tasks.StateFailed
	if errors.Is(err, context.Canceled) {
		state = tasks.StateCancelled
	} else if errors.Is(err, context.DeadlineExceeded) {
		state = tasks.StateTimedOut
	}
	req.tm.Finish(req.task.ID, state, err)
}

// AllStats returns statistics for all managed pools.
func (m *Manager) AllStats() []PoolStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]PoolStats, 0, len(m.pools))
	for _, pool := range m.pools {
		result = append(result, pool.Stats())
	}
	return result
}
