package workers

import (
	"context"
	"fmt"
	"sync"

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
	mu    sync.RWMutex
	pools map[string]*Pool
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

// Submit dispatches a task to the designated worker pool.
func (m *Manager) Submit(ctx context.Context, poolName string, task tasks.Task) error {
	pool, ok := m.Get(poolName)
	if !ok {
		return fmt.Errorf("worker pool %q not found", poolName)
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
