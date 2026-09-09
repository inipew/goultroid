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
	PoolEvent        = "event"
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
}

// NewManager creates a Manager initialized with standard workload pools.
func NewManager() *Manager {
	m := &Manager{
		pools: make(map[string]*Pool),
	}

	// Initialize default isolated pools per Blueprint §30
	m.pools[PoolEvent] = NewPool(PoolEvent, 16, 500, queue.PolicyBlock)
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
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, pool := range m.pools {
		pool.Start(ctx)
	}
	return nil
}

// Stop gracefully stops all managed worker pools.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var stopErrs []error
	for _, pool := range m.pools {
		if err := pool.Stop(ctx); err != nil {
			stopErrs = append(stopErrs, err)
		}
	}

	if len(stopErrs) > 0 {
		return fmt.Errorf("worker manager stop encountered errors: %v", stopErrs)
	}
	return nil
}

// Health probes the health status of all worker pools.
func (m *Manager) Health(ctx context.Context) runtime.ComponentHealth {
	m.mu.RLock()
	defer m.mu.RUnlock()

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
	pool, ok := m.Get(poolName)
	if !ok {
		return fmt.Errorf("worker pool %q not found", poolName)
	}

	m.mu.RLock()
	tm := m.tasksManager
	m.mu.RUnlock()

	if tm != nil {
		if task.ID == "" {
			task.ID = fmt.Sprintf("task:%s:%d", poolName, time.Now().UnixNano())
		}
		taskCtx, cancel, err := tm.Register(ctx, task)
		if err != nil {
			return err
		}

		origRun := task.Run
		task.Run = func(runCtx context.Context) error {
			defer cancel()
			startCtx, err := tm.Start(task.ID)
			if err != nil {
				tm.Finish(task.ID, tasks.StateFailed, err)
				return err
			}

			execCtx, execCancel := context.WithCancel(startCtx)
			defer execCancel()

			var runErr error
			if origRun != nil {
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

		return pool.Submit(taskCtx, task)
	}

	return pool.Submit(ctx, task)
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
