package tasks

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrQuotaExceeded = errors.New("task quota exceeded for owner")
	ErrTaskNotFound  = errors.New("task not found")
	ErrTaskExists    = errors.New("task already registered")
)

// Quota defines concurrency and queue limits for a task owner (e.g. a plugin).
type Quota struct {
	MaxConcurrent int `json:"max_concurrent"`
	MaxQueued     int `json:"max_queued"`
}

// Default quotas
var DefaultQuota = Quota{
	MaxConcurrent: 10,
	MaxQueued:     50,
}

// OwnerStats summarizes current activity and historical results for an owner.
type OwnerStats struct {
	Owner     string `json:"owner"`
	Queued    int    `json:"queued"`
	Running   int    `json:"running"`
	Completed int64  `json:"completed"`
	Failed    int64  `json:"failed"`
	Cancelled int64  `json:"cancelled"`
	TimedOut  int64  `json:"timed_out"`
}

// TaskStats summarizes aggregate execution metrics across all tasks.
type TaskStats struct {
	TotalQueued    int                   `json:"total_queued"`
	TotalRunning   int                   `json:"total_running"`
	TotalCompleted int64                 `json:"total_completed"`
	TotalFailed    int64                 `json:"total_failed"`
	TotalCancelled int64                 `json:"total_cancelled"`
	TotalTimedOut  int64                 `json:"total_timed_out"`
	Owners         map[string]OwnerStats `json:"owners"`
}

type trackedTask struct {
	task   Task
	ctx    context.Context
	cancel context.CancelFunc
}

// Manager tracks task lifecycles, enforces per-owner execution quotas,
// and provides task cancellation by task ID or owner.
type Manager struct {
	mu           sync.RWMutex
	defaultQuota Quota
	quotas       map[string]Quota

	activeTasks map[string]*trackedTask
	ownerCounts map[string]*OwnerStats

	totalCompleted int64
	totalFailed    int64
	totalCancelled int64
	totalTimedOut  int64
}

// NewManager creates a new Task Manager with standard default quotas.
func NewManager() *Manager {
	return &Manager{
		defaultQuota: DefaultQuota,
		quotas:       make(map[string]Quota),
		activeTasks:  make(map[string]*trackedTask),
		ownerCounts:  make(map[string]*OwnerStats),
	}
}

// SetDefaultQuota updates the default quota applied to owners with no custom quota.
func (m *Manager) SetDefaultQuota(q Quota) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultQuota = q
}

// SetOwnerQuota sets quota limits for a specific owner.
func (m *Manager) SetOwnerQuota(owner string, q Quota) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quotas[owner] = q
}

// GetQuota returns the quota for an owner.
func (m *Manager) GetQuota(owner string) Quota {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if q, ok := m.quotas[owner]; ok {
		return q
	}
	return m.defaultQuota
}

func (m *Manager) getOwnerStatsLocked(owner string) *OwnerStats {
	stats, ok := m.ownerCounts[owner]
	if !ok {
		stats = &OwnerStats{Owner: owner}
		m.ownerCounts[owner] = stats
	}
	return stats
}

// Register registers a task in StateQueued and allocates a cancellable child context.
// Returns an error if the owner exceeds their queue quota or if the task is already registered.
func (m *Manager) Register(parentCtx context.Context, task Task) (context.Context, context.CancelFunc, error) {
	if err := task.Validate(); err != nil {
		return nil, nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.activeTasks[task.ID]; exists {
		return nil, nil, fmt.Errorf("%w: %s", ErrTaskExists, task.ID)
	}

	quota := m.defaultQuota
	if q, ok := m.quotas[task.Owner]; ok {
		quota = q
	}

	ownerStats := m.getOwnerStatsLocked(task.Owner)
	if quota.MaxQueued > 0 && ownerStats.Queued >= quota.MaxQueued {
		return nil, nil, fmt.Errorf("%w: max queued limit %d reached for %s", ErrQuotaExceeded, quota.MaxQueued, task.Owner)
	}

	task.State = StateQueued
	task.CreatedAt = time.Now().UTC()

	ctx, cancel := context.WithCancel(parentCtx)
	tracked := &trackedTask{
		task:   task,
		ctx:    ctx,
		cancel: cancel,
	}

	m.activeTasks[task.ID] = tracked
	ownerStats.Queued++

	return ctx, cancel, nil
}

// Start transitions a queued task to StateRunning, checking the concurrent execution quota.
func (m *Manager) Start(taskID string) (context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tracked, exists := m.activeTasks[taskID]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}

	quota := m.defaultQuota
	if q, ok := m.quotas[tracked.task.Owner]; ok {
		quota = q
	}

	ownerStats := m.getOwnerStatsLocked(tracked.task.Owner)
	if quota.MaxConcurrent > 0 && ownerStats.Running >= quota.MaxConcurrent {
		return nil, fmt.Errorf("%w: max concurrent limit %d reached for %s", ErrQuotaExceeded, quota.MaxConcurrent, tracked.task.Owner)
	}

	if tracked.task.State == StateQueued {
		ownerStats.Queued--
	}
	ownerStats.Running++

	tracked.task.State = StateRunning
	tracked.task.StartedAt = time.Now().UTC()

	return tracked.ctx, nil
}

// Finish records task completion, updates metrics, and releases quota.
func (m *Manager) Finish(taskID string, finalState TaskState, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tracked, exists := m.activeTasks[taskID]
	if !exists {
		return
	}

	ownerStats := m.getOwnerStatsLocked(tracked.task.Owner)
	if tracked.task.State == StateRunning {
		ownerStats.Running--
	} else if tracked.task.State == StateQueued {
		ownerStats.Queued--
	}

	tracked.task.CompletedAt = time.Now().UTC()
	tracked.task.State = finalState
	tracked.task.Error = err

	switch finalState {
	case StateCompleted:
		m.totalCompleted++
		ownerStats.Completed++
	case StateFailed:
		m.totalFailed++
		ownerStats.Failed++
	case StateCancelled:
		m.totalCancelled++
		ownerStats.Cancelled++
	case StateTimedOut:
		m.totalTimedOut++
		ownerStats.TimedOut++
	}

	delete(m.activeTasks, taskID)
}

// Cancel cancels an individual active task by ID.
func (m *Manager) Cancel(taskID string) bool {
	m.mu.RLock()
	tracked, exists := m.activeTasks[taskID]
	m.mu.RUnlock()

	if !exists {
		return false
	}

	tracked.cancel()
	return true
}

// CancelByOwner cancels all active (running or queued) tasks for the given owner.
// Returns the number of tasks cancelled.
func (m *Manager) CancelByOwner(owner string) int {
	m.mu.RLock()
	var cancels []context.CancelFunc
	for _, tracked := range m.activeTasks {
		if tracked.task.Owner == owner {
			cancels = append(cancels, tracked.cancel)
		}
	}
	m.mu.RUnlock()

	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
}

// GetTask returns a copy of the active task if found.
func (m *Manager) GetTask(taskID string) (Task, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tracked, ok := m.activeTasks[taskID]
	if !ok {
		return Task{}, false
	}
	return tracked.task, true
}

// Stats returns a snapshot of execution metrics across all tasks.
func (m *Manager) Stats() TaskStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	totalQueued := 0
	totalRunning := 0
	ownerCopies := make(map[string]OwnerStats, len(m.ownerCounts))

	for k, v := range m.ownerCounts {
		ownerCopies[k] = *v
		totalQueued += v.Queued
		totalRunning += v.Running
	}

	return TaskStats{
		TotalQueued:    totalQueued,
		TotalRunning:   totalRunning,
		TotalCompleted: m.totalCompleted,
		TotalFailed:    m.totalFailed,
		TotalCancelled: m.totalCancelled,
		TotalTimedOut:  m.totalTimedOut,
		Owners:         ownerCopies,
	}
}
