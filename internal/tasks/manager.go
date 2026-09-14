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
	Admitted  int    `json:"admitted"`
	Queued    int    `json:"queued"`
	Running   int    `json:"running"`
	Completed int64  `json:"completed"`
	Failed    int64  `json:"failed"`
	Cancelled int64  `json:"cancelled"`
	TimedOut  int64  `json:"timed_out"`
}

// TaskStats summarizes aggregate execution metrics across all tasks.
type TaskStats struct {
	TotalAdmitted  int                   `json:"total_admitted"`
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
	// ownerActive tracks owner concurrency reservations. A reservation is held
	// from Queued through Running so physical workers never have to block after
	// popping work merely to acquire an owner slot.
	ownerActive map[string]int
	slotChanged chan struct{}

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
		ownerActive:  make(map[string]int),
		slotChanged:  make(chan struct{}),
	}
}

// SetDefaultQuota updates the default quota applied to owners with no custom quota.
func (m *Manager) SetDefaultQuota(q Quota) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultQuota = q
	m.notifySlotChangeLocked()
}

// SetOwnerQuota sets quota limits for a specific owner.
func (m *Manager) SetOwnerQuota(owner string, q Quota) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quotas[owner] = q
	m.notifySlotChangeLocked()
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

// Register accepts a task into logical admission. It does not claim an owner
// execution slot and it does not imply physical queueing or execution.
func (m *Manager) Register(parentCtx context.Context, task Task) (context.Context, context.CancelFunc, error) {
	if err := task.Validate(); err != nil {
		return nil, nil, err
	}
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.activeTasks[task.ID]; exists {
		return nil, nil, fmt.Errorf("%w: %s", ErrTaskExists, task.ID)
	}

	quota := m.quotaLocked(task.Owner)
	ownerStats := m.getOwnerStatsLocked(task.Owner)
	// MaxQueued limits work that has not started physically. Admitted work and
	// physically queued work both consume this queue budget; Running does not.
	if quota.MaxQueued > 0 && ownerStats.Admitted+ownerStats.Queued >= quota.MaxQueued {
		return nil, nil, fmt.Errorf("%w: max queued limit %d reached for %s", ErrQuotaExceeded, quota.MaxQueued, task.Owner)
	}

	now := time.Now().UTC()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	task.AdmittedAt = now
	task.State = StateAdmitted

	ctx, cancel := context.WithCancel(parentCtx)
	m.activeTasks[task.ID] = &trackedTask{task: task, ctx: ctx, cancel: cancel}
	ownerStats.Admitted++
	return ctx, cancel, nil
}

// TryQueue reserves one owner concurrency slot and transitions an admitted task
// into the physical-queue lifecycle state. It never waits.
func (m *Manager) TryQueue(taskID string) (context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queueLocked(taskID)
}

func (m *Manager) queueLocked(taskID string) (context.Context, error) {
	tracked, exists := m.activeTasks[taskID]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if err := tracked.ctx.Err(); err != nil {
		return nil, err
	}
	if tracked.task.State != StateAdmitted {
		return nil, fmt.Errorf("task %s cannot queue from state %s", taskID, tracked.task.State)
	}

	quota := m.quotaLocked(tracked.task.Owner)
	active := m.ownerActive[tracked.task.Owner]
	if quota.MaxConcurrent > 0 && active >= quota.MaxConcurrent {
		return nil, fmt.Errorf("%w: max concurrent limit %d reached for %s", ErrQuotaExceeded, quota.MaxConcurrent, tracked.task.Owner)
	}

	ownerStats := m.getOwnerStatsLocked(tracked.task.Owner)
	ownerStats.Admitted--
	ownerStats.Queued++
	m.ownerActive[tracked.task.Owner] = active + 1
	tracked.task.State = StateQueued
	tracked.task.QueuedAt = time.Now().UTC()
	return tracked.ctx, nil
}

// MarkRunning records physical execution start. This must be called only after
// a worker has popped the task and is about to enter its execution body.
func (m *Manager) MarkRunning(taskID string) (context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.markRunningLocked(taskID)
}

func (m *Manager) markRunningLocked(taskID string) (context.Context, error) {
	tracked, exists := m.activeTasks[taskID]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if err := tracked.ctx.Err(); err != nil {
		return nil, err
	}
	if tracked.task.State != StateQueued {
		return nil, fmt.Errorf("task %s cannot run from state %s", taskID, tracked.task.State)
	}
	ownerStats := m.getOwnerStatsLocked(tracked.task.Owner)
	ownerStats.Queued--
	ownerStats.Running++
	tracked.task.State = StateRunning
	tracked.task.StartedAt = time.Now().UTC()
	return tracked.ctx, nil
}

// TryStart is retained for direct TaskManager users. Production WorkerManager
// uses TryQueue followed by MarkRunning so Running reflects physical execution.
func (m *Manager) TryStart(taskID string) (context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.queueLocked(taskID); err != nil {
		return nil, err
	}
	return m.markRunningLocked(taskID)
}

// WaitStart is retained as a compatibility API for direct TaskManager users.
// WorkerManager no longer creates one waiter per task; its bounded admission
// controllers use TryQueue and SlotChanges instead.
func (m *Manager) WaitStart(waitCtx context.Context, taskID string) (context.Context, error) {
	if waitCtx == nil {
		waitCtx = context.Background()
	}
	for {
		m.mu.Lock()
		tracked, exists := m.activeTasks[taskID]
		if !exists {
			m.mu.Unlock()
			return nil, fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
		}
		if err := waitCtx.Err(); err != nil {
			m.mu.Unlock()
			return nil, err
		}
		if err := tracked.ctx.Err(); err != nil {
			m.mu.Unlock()
			return nil, err
		}

		quota := m.quotaLocked(tracked.task.Owner)
		if quota.MaxConcurrent <= 0 || m.ownerActive[tracked.task.Owner] < quota.MaxConcurrent {
			ctx, err := m.queueLocked(taskID)
			if err == nil {
				ctx, err = m.markRunningLocked(taskID)
			}
			m.mu.Unlock()
			return ctx, err
		}

		slotChanged := m.slotChanged
		taskCtx := tracked.ctx
		m.mu.Unlock()
		select {
		case <-slotChanged:
		case <-taskCtx.Done():
			return nil, taskCtx.Err()
		case <-waitCtx.Done():
			return nil, waitCtx.Err()
		}
	}
}

// SlotChanges returns a generation channel closed whenever owner execution
// capacity or quota configuration changes. A fixed number of admission
// controllers wait on this channel instead of one waiter per task.
func (m *Manager) SlotChanges() <-chan struct{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.slotChanged
}

func (m *Manager) quotaLocked(owner string) Quota {
	if q, ok := m.quotas[owner]; ok {
		return q
	}
	return m.defaultQuota
}

func (m *Manager) notifySlotChangeLocked() {
	close(m.slotChanged)
	m.slotChanged = make(chan struct{})
}

// Finish records task completion, updates metrics, and releases any owner slot.
func (m *Manager) Finish(taskID string, finalState TaskState, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tracked, exists := m.activeTasks[taskID]
	if !exists {
		return
	}

	ownerStats := m.getOwnerStatsLocked(tracked.task.Owner)
	releaseSlot := false
	switch tracked.task.State {
	case StateAdmitted:
		ownerStats.Admitted--
	case StateQueued:
		ownerStats.Queued--
		releaseSlot = true
	case StateRunning:
		ownerStats.Running--
		releaseSlot = true
	}
	if releaseSlot {
		if active := m.ownerActive[tracked.task.Owner]; active > 1 {
			m.ownerActive[tracked.task.Owner] = active - 1
		} else {
			delete(m.ownerActive, tracked.task.Owner)
		}
		m.notifySlotChangeLocked()
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

// CancelByOwner cancels all active tasks for the given owner.
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

// CancelByCorrelationID cancels every active task sharing correlationID.
func (m *Manager) CancelByCorrelationID(correlationID string) int {
	if correlationID == "" {
		return 0
	}
	m.mu.RLock()
	var cancels []context.CancelFunc
	for _, tracked := range m.activeTasks {
		if tracked.task.CorrelationID == correlationID {
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

	totalAdmitted := 0
	totalQueued := 0
	totalRunning := 0
	ownerCopies := make(map[string]OwnerStats, len(m.ownerCounts))
	for k, v := range m.ownerCounts {
		ownerCopies[k] = *v
		totalAdmitted += v.Admitted
		totalQueued += v.Queued
		totalRunning += v.Running
	}
	return TaskStats{
		TotalAdmitted:  totalAdmitted,
		TotalQueued:    totalQueued,
		TotalRunning:   totalRunning,
		TotalCompleted: m.totalCompleted,
		TotalFailed:    m.totalFailed,
		TotalCancelled: m.totalCancelled,
		TotalTimedOut:  m.totalTimedOut,
		Owners:         ownerCopies,
	}
}
