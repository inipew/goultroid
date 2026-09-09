package jobs

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
)

// Ensure Manager implements runtime.Component.
var _ runtime.Component = (*Manager)(nil)

// TaskSubmitter is the interface used by JobManager to queue tasks into worker pools.
type TaskSubmitter interface {
	Submit(ctx context.Context, poolName string, task tasks.Task) error
}

// IdempotencyClaimer is the interface used by JobManager to claim and deduplicate job execution.
type IdempotencyClaimer interface {
	CheckAndSet(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

// JobHandler executes work for a declarative job type.
type JobHandler func(ctx context.Context, j *Job) error

// Diagnostics provides runtime statistics for jobs.
type Diagnostics struct {
	Registered       int      `json:"registered"`
	Running          int      `json:"running"`
	Completed        int      `json:"completed"`
	Failed           int      `json:"failed"`
	Cancelled        int      `json:"cancelled"`
	Total            int      `json:"total"`
	RecoveryFailures int      `json:"recovery_failures"`
	RecoveryErrors   []string `json:"recovery_errors,omitempty"`
}

// Manager coordinates declarative jobs and delegates their execution to workers as tasks.
type Manager struct {
	mu               sync.RWMutex
	jobs             map[string]*Job
	handlers         map[string]JobHandler
	submitter        TaskSubmitter
	repo             Repository
	idemp            IdempotencyClaimer
	recoveryFailures int
	recoveryErrors   []string
	terminalAt       map[string]time.Time
	retention        time.Duration
	cleanupInterval  time.Duration
	cleanupCancel    context.CancelFunc
	cleanupWG        sync.WaitGroup
}

// NewManager creates a new JobManager backed by a task submitter and optional repository.
func NewManager(submitter TaskSubmitter, repo ...Repository) *Manager {
	m := &Manager{
		jobs:            make(map[string]*Job),
		handlers:        make(map[string]JobHandler),
		submitter:       submitter,
		terminalAt:      make(map[string]time.Time),
		retention:       7 * 24 * time.Hour,
		cleanupInterval: time.Hour,
	}
	if len(repo) > 0 && repo[0] != nil {
		m.repo = repo[0]
	}
	return m
}

// SetRetention configures how long terminal jobs are retained and how often cleanup runs.
func (m *Manager) SetRetention(retention, cleanupInterval time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if retention > 0 {
		m.retention = retention
	}
	if cleanupInterval > 0 {
		m.cleanupInterval = cleanupInterval
	}
}

// SetRepository configures durable persistence for the manager.
func (m *Manager) SetRepository(repo Repository) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.repo = repo
}

// SetIdempotencyManager configures an idempotency manager for deduplicating job execution.
func (m *Manager) SetIdempotencyManager(idemp IdempotencyClaimer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idemp = idemp
}

// RegisterHandler registers a typed execution handler for declarative jobs.
func (m *Manager) RegisterHandler(jobType string, handler JobHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[jobType] = handler
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

	if j.IdempotencyKey != "" {
		for _, existing := range m.jobs {
			if existing.IdempotencyKey == j.IdempotencyKey &&
				(existing.State == StateRegistered || existing.State == StateTriggered || existing.State == StateExecuting) {
				return fmt.Errorf("active job with idempotency key %q already exists: %s", j.IdempotencyKey, existing.ID)
			}
		}
	}

	if j.Run == nil && j.Type != "" {
		if handler, ok := m.handlers[j.Type]; ok {
			h := handler
			j.Run = func(ctx context.Context) error {
				return h(ctx, &j)
			}
		} else {
			return fmt.Errorf("no handler registered for job type %q", j.Type)
		}
	}

	if j.Pool == "" {
		j.Pool = workers.PoolGeneral
	}
	j.State = StateRegistered
	m.jobs[j.ID] = &j

	if m.repo != nil {
		if err := m.repo.Save(context.Background(), &j); err != nil {
			return fmt.Errorf("failed to persist job %q: %w", j.ID, err)
		}
	}

	return nil
}

// Trigger converts a registered job into a concrete Task and queues it into the worker pool.
func (m *Manager) Trigger(ctx context.Context, jobID string) error {
	m.mu.Lock()
	job, exists := m.jobs[jobID]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("job %q not found", jobID)
	}

	previousState := job.State
	previousLastRun := job.LastRun
	job.State = StateTriggered
	job.LastRun = time.Now().UTC()
	runFn := job.Run
	owner := job.Owner
	timeout := job.Timeout
	idempotencyKey := job.IdempotencyKey
	idemp := m.idemp
	pool := job.Pool
	if pool == "" {
		pool = workers.PoolGeneral
	}
	lastRun := job.LastRun
	nextRun := job.NextRun
	repo := m.repo
	m.mu.Unlock()

	if repo != nil {
		_ = repo.UpdateState(ctx, jobID, StateTriggered, "", lastRun, nextRun)
	}

	if m.submitter == nil {
		m.rollbackTrigger(ctx, job, previousState, previousLastRun, "task submitter not configured")
		return fmt.Errorf("task submitter not configured")
	}

	task := tasks.Task{
		ID:             fmt.Sprintf("task:%s:%d", jobID, time.Now().UnixNano()),
		Owner:          owner,
		Name:           "job:" + jobID,
		Timeout:        timeout,
		IdempotencyKey: idempotencyKey,
		Run: func(taskCtx context.Context) error {
			if idemp != nil && idempotencyKey != "" {
				first, err := idemp.CheckAndSet(taskCtx, idempotencyKey, 1*time.Hour)
				if err == nil && !first {
					m.mu.Lock()
					job.State = StateCompleted
					job.LastError = "skipped: duplicate execution detected by idempotency key"
					m.terminalAt[jobID] = time.Now().UTC()
					m.mu.Unlock()
					if repo != nil {
						_ = repo.UpdateState(context.Background(), jobID, StateCompleted, job.LastError, job.LastRun, job.NextRun)
					}
					return nil
				}
			}

			m.mu.Lock()
			job.State = StateExecuting
			m.mu.Unlock()
			if repo != nil {
				_ = repo.UpdateState(taskCtx, jobID, StateExecuting, "", job.LastRun, job.NextRun)
			}

			var err error
			if runFn != nil {
				err = runFn(taskCtx)
			}

			m.mu.Lock()
			if err != nil {
				job.State = StateFailed
				job.LastError = err.Error()
			} else {
				job.State = StateCompleted
				job.LastError = ""
			}
			m.terminalAt[jobID] = time.Now().UTC()
			jobState := job.State
			jobErr := job.LastError
			jLastRun := job.LastRun
			jNextRun := job.NextRun
			m.mu.Unlock()

			if repo != nil {
				_ = repo.UpdateState(context.Background(), jobID, jobState, jobErr, jLastRun, jNextRun)
			}

			return err
		},
	}

	if err := m.submitter.Submit(ctx, pool, task); err != nil {
		m.rollbackTrigger(ctx, job, previousState, previousLastRun, err.Error())
		return fmt.Errorf("submit job %q to pool %q: %w", jobID, pool, err)
	}
	return nil
}

func (m *Manager) rollbackTrigger(ctx context.Context, job *Job, state JobState, lastRun time.Time, reason string) {
	m.mu.Lock()
	job.State = state
	job.LastRun = lastRun
	job.LastError = reason
	nextRun := job.NextRun
	repo := m.repo
	m.mu.Unlock()
	if repo != nil {
		persistCtx := context.Background()
		if ctx != nil {
			persistCtx = context.WithoutCancel(ctx)
		}
		_ = repo.UpdateState(persistCtx, job.ID, state, reason, lastRun, nextRun)
	}
}

// Cancel cancels and removes a specific job.
func (m *Manager) Cancel(ctx context.Context, jobID string) error {
	m.mu.Lock()
	job, exists := m.jobs[jobID]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("job %q not found", jobID)
	}
	job.State = StateCancelled
	delete(m.jobs, jobID)
	repo := m.repo
	m.mu.Unlock()

	if repo != nil {
		if err := repo.Delete(ctx, jobID); err != nil {
			return err
		}
	}
	return nil
}

// CancelByOwner cancels and removes all jobs belonging to a specific owner (e.g. on plugin disable).
func (m *Manager) CancelByOwner(owner string) int {
	m.mu.Lock()
	cancelled := 0
	for id, j := range m.jobs {
		if j.Owner == owner {
			j.State = StateCancelled
			delete(m.jobs, id)
			cancelled++
		}
	}
	repo := m.repo
	m.mu.Unlock()

	if repo != nil {
		_, _ = repo.DeleteByOwner(context.Background(), owner)
	}
	return cancelled
}

// LoadAndReconcile loads persisted active jobs and recovers missed executions.
func (m *Manager) LoadAndReconcile(ctx context.Context) error {
	m.mu.RLock()
	repo := m.repo
	m.mu.RUnlock()

	if repo == nil {
		return nil
	}

	activeJobs, err := repo.ListActive(ctx)
	if err != nil {
		return fmt.Errorf("failed to list active jobs for reconciliation: %w", err)
	}

	now := time.Now().UTC()
	var toTrigger []string

	m.mu.Lock()
	for _, j := range activeJobs {
		if j.Run == nil && j.Type != "" {
			if handler, ok := m.handlers[j.Type]; ok {
				h := handler
				jobRef := j
				j.Run = func(runCtx context.Context) error {
					return h(runCtx, jobRef)
				}
			}
		}

		if j.State == StateExecuting || j.State == StateTriggered {
			// Job was interrupted mid-run
			switch j.RecoveryPolicy {
			case RecoveryRunImmediately:
				j.State = StateRegistered
				m.jobs[j.ID] = j
				toTrigger = append(toTrigger, j.ID)
			case RecoverySkip:
				j.State = StateRegistered
				m.jobs[j.ID] = j
			case RecoveryRecalculate:
				j.State = StateRegistered
				m.jobs[j.ID] = j
			default:
				j.State = StateRegistered
				m.jobs[j.ID] = j
			}
		} else {
			// Registered / scheduled
			if !j.NextRun.IsZero() && j.NextRun.Before(now) {
				switch j.RecoveryPolicy {
				case RecoveryRunImmediately:
					m.jobs[j.ID] = j
					toTrigger = append(toTrigger, j.ID)
				default:
					m.jobs[j.ID] = j
				}
			} else {
				m.jobs[j.ID] = j
			}
		}
	}
	m.mu.Unlock()

	for _, jobID := range toTrigger {
		if err := m.Trigger(ctx, jobID); err != nil {
			m.mu.Lock()
			m.recoveryFailures++
			m.recoveryErrors = append(m.recoveryErrors, fmt.Sprintf("%s: %v", jobID, err))
			m.mu.Unlock()
		}
	}

	if m.recoveryFailures > 0 {
		return fmt.Errorf("reconciliation completed with %d failure(s)", m.recoveryFailures)
	}

	return nil
}

// Diagnostics returns aggregate job execution statistics.
func (m *Manager) Diagnostics() Diagnostics {
	m.mu.RLock()
	defer m.mu.RUnlock()

	recErrs := make([]string, len(m.recoveryErrors))
	copy(recErrs, m.recoveryErrors)

	d := Diagnostics{
		Total:            len(m.jobs),
		RecoveryFailures: m.recoveryFailures,
		RecoveryErrors:   recErrs,
	}
	for _, j := range m.jobs {
		switch j.State {
		case StateRegistered, StateScheduled:
			d.Registered++
		case StateTriggered, StateExecuting:
			d.Running++
		case StateCompleted:
			d.Completed++
		case StateFailed:
			d.Failed++
		case StateCancelled:
			d.Cancelled++
		}
	}
	return d
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

// Name returns the component name for runtime.Component.
func (m *Manager) Name() string {
	return "jobs"
}

// Dependencies returns component prerequisites for runtime.Component.
func (m *Manager) Dependencies() []string {
	return []string{"workers"}
}

// Start reconciles active jobs on runtime startup.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.LoadAndReconcile(ctx); err != nil {
		return err
	}
	if _, err := m.CleanupTerminal(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	if m.cleanupCancel != nil {
		m.mu.Unlock()
		return nil
	}
	cleanupCtx, cancel := context.WithCancel(ctx)
	m.cleanupCancel = cancel
	interval := m.cleanupInterval
	m.cleanupWG.Add(1)
	m.mu.Unlock()
	go m.cleanupLoop(cleanupCtx, interval)
	return nil
}

// Stop gracefully terminates job execution.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	cancel := m.cleanupCancel
	m.cleanupCancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		m.cleanupWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) cleanupLoop(ctx context.Context, interval time.Duration) {
	defer m.cleanupWG.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = m.CleanupTerminal(ctx)
		}
	}
}

// CleanupTerminal removes terminal jobs whose retention window has elapsed.
func (m *Manager) CleanupTerminal(ctx context.Context) (int, error) {
	m.mu.Lock()
	cutoff := time.Now().UTC().Add(-m.retention)
	removed := 0
	for id, completedAt := range m.terminalAt {
		if !completedAt.After(cutoff) {
			delete(m.jobs, id)
			delete(m.terminalAt, id)
			removed++
		}
	}
	repo := m.repo
	m.mu.Unlock()
	if repo != nil {
		persisted, err := repo.DeleteTerminalBefore(ctx, cutoff)
		removed += persisted
		if err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// Health probes job manager health.
func (m *Manager) Health(ctx context.Context) runtime.ComponentHealth {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.recoveryFailures > 0 {
		return runtime.ComponentHealth{
			Status:  runtime.HealthDegraded,
			Details: fmt.Sprintf("%d job recovery failure(s): %s", m.recoveryFailures, strings.Join(m.recoveryErrors, "; ")),
			Error:   fmt.Errorf("job reconciliation completed with %d failure(s)", m.recoveryFailures),
		}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}
