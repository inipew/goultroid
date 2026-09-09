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

// Manager coordinates isolated worker pools across the application runtime.
type Manager struct {
	mu           sync.RWMutex
	pools        map[string]*Pool
	tasksManager *tasks.Manager
	accepting    bool
	acceptingEnd chan struct{}
	admissionCtx context.Context
	admissionEnd context.CancelFunc
	admissionWG  sync.WaitGroup
}

// NewManager creates a Manager initialized with standard workload pools.
func NewManager() *Manager {
	m := &Manager{
		pools: make(map[string]*Pool),
	}

	// Initialize default isolated pools per Blueprint §30
	m.pools[PoolGeneral] = NewPool(PoolGeneral, 8, 200, queue.PolicyBlock)
	m.pools[PoolDownload] = NewPool(PoolDownload, 3, 50, queue.PolicyReject)
	m.pools[PoolMediaProcess] = NewPool(PoolMediaProcess, 2, 20, queue.PolicyReject)
	m.pools[PoolScheduler] = NewPool(PoolScheduler, 4, 100, queue.PolicyBlock)

	return m
}

func (m *Manager) Name() string {
	return "workers"
}

func (m *Manager) Dependencies() []string {
	return nil
}

// Start starts all managed worker pools.
func (m *Manager) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	m.admissionCtx, m.admissionEnd = context.WithCancel(ctx)
	m.accepting = true
	m.acceptingEnd = make(chan struct{})
	pools := make([]*Pool, 0, len(m.pools))
	for _, pool := range m.pools {
		pools = append(pools, pool)
	}
	m.mu.Unlock()

	for _, pool := range pools {
		pool.Start(ctx)
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
		<-admissionsDone
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

	return runtime.ComponentHealth{
		Status: runtime.HealthHealthy,
	}
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
	admissionCtx := m.admissionCtx
	m.mu.RUnlock()
	if !accepting {
		return fmt.Errorf("worker manager is not accepting tasks")
	}

	if tm != nil {
		if err := pool.reserveAdmission(ctx, acceptingEnd); err != nil {
			return fmt.Errorf("pool %s admission rejected: %w", poolName, err)
		}
		m.mu.Lock()
		if !m.accepting || m.acceptingEnd != acceptingEnd {
			m.mu.Unlock()
			pool.releaseAdmission()
			return fmt.Errorf("worker manager is not accepting tasks")
		}
		if task.ID == "" {
			task.ID = fmt.Sprintf("task:%s:%d", poolName, time.Now().UnixNano())
		}
		taskCtx, cancel, err := tm.Register(ctx, task)
		if err != nil {
			pool.releaseAdmission()
			m.mu.Unlock()
			return err
		}
		m.admissionWG.Add(1)
		m.mu.Unlock()

		origRun := task.Run
		task.Run = func(runCtx context.Context) error {
			defer cancel()
			execCtx, execCancel := context.WithCancel(taskCtx)
			stopPoolCancel := context.AfterFunc(runCtx, execCancel)
			defer stopPoolCancel()
			defer execCancel()

			var runErr error
			if err := execCtx.Err(); err != nil {
				runErr = err
			} else if origRun != nil {
				runErr = origRun(execCtx)
			}

			state := tasks.StateCompleted
			if runErr != nil {
				if errors.Is(runErr, context.Canceled) {
					state = tasks.StateCancelled
				} else {
					state = tasks.StateFailed
				}
			}
			tm.Finish(task.ID, state, runErr)
			return runErr
		}

		go m.admitTask(admissionCtx, pool, tm, taskCtx, cancel, task)
		return nil
	}

	return pool.Submit(ctx, task)
}

func (m *Manager) admitTask(admissionCtx context.Context, pool *Pool, tm *tasks.Manager, taskCtx context.Context, cancel context.CancelFunc, task tasks.Task) {
	defer m.admissionWG.Done()
	defer pool.releaseAdmission()
	startCtx, err := tm.WaitStart(admissionCtx, task.ID)
	if err == nil {
		enqueueCtx, enqueueCancel := context.WithCancel(startCtx)
		stopAdmission := context.AfterFunc(admissionCtx, enqueueCancel)
		err = pool.submitAccepted(enqueueCtx, task)
		stopAdmission()
		enqueueCancel()
	}
	if err == nil {
		return
	}
	cancel()
	state := tasks.StateFailed
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(taskCtx.Err(), context.Canceled) {
		state = tasks.StateCancelled
	}
	tm.Finish(task.ID, state, err)
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
