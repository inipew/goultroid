package jobs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
)

// TaskSubmitter is the interface used by JobManager to queue tasks into worker pools.
type TaskSubmitter interface {
	Submit(ctx context.Context, poolName string, task tasks.Task) error
}

// Manager coordinates declarative jobs and delegates their execution to workers as tasks.
type Manager struct {
	mu        sync.RWMutex
	jobs      map[string]*Job
	submitter TaskSubmitter
}

// NewManager creates a new JobManager backed by a task submitter (e.g. workers.Manager).
func NewManager(submitter TaskSubmitter) *Manager {
	return &Manager{
		jobs:      make(map[string]*Job),
		submitter: submitter,
	}
}

// Register registers a declarative job with the manager.
func (m *Manager) Register(j Job) error {
	if err := j.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.jobs[j.ID]; exists {
		return fmt.Errorf("job %q already registered", j.ID)
	}

	j.State = StateRegistered
	m.jobs[j.ID] = &j
	return nil
}

// Trigger converts a triggered job into a concrete Task and queues it into the worker pool.
func (m *Manager) Trigger(ctx context.Context, jobID string) error {
	m.mu.Lock()
	job, exists := m.jobs[jobID]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("job %q not found", jobID)
	}

	job.State = StateTriggered
	job.LastRun = time.Now().UTC()
	runFn := job.Run
	owner := job.Owner
	timeout := job.Timeout
	idempotencyKey := job.IdempotencyKey
	m.mu.Unlock()

	if m.submitter == nil {
		return fmt.Errorf("task submitter not configured")
	}

	task := tasks.Task{
		ID:             fmt.Sprintf("task:%s:%d", jobID, time.Now().UnixNano()),
		Owner:          owner,
		Name:           "job:" + jobID,
		Timeout:        timeout,
		IdempotencyKey: idempotencyKey,
		Run:            runFn,
	}

	return m.submitter.Submit(ctx, workers.PoolGeneral, task)
}

// CancelByOwner cancels and removes all jobs belonging to a specific owner (e.g. on plugin disable).
func (m *Manager) CancelByOwner(owner string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	cancelled := 0
	for id, j := range m.jobs {
		if j.Owner == owner {
			j.State = StateCancelled
			delete(m.jobs, id)
			cancelled++
		}
	}
	return cancelled
}

// Get returns the job by ID, if present.
func (m *Manager) Get(jobID string) (Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, exists := m.jobs[jobID]
	if !exists {
		return Job{}, false
	}
	return *j, true
}

// ByOwner returns all jobs for a specific owner.
func (m *Manager) ByOwner(owner string) []Job {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []Job
	for _, j := range m.jobs {
		if j.Owner == owner {
			result = append(result, *j)
		}
	}
	return result
}

// All returns a slice of all registered jobs.
func (m *Manager) All() []Job {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		result = append(result, *j)
	}
	return result
}
