package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
	"go.uber.org/zap"
)

type MisfirePolicy int

const (
	MisfireRunOnce MisfirePolicy = iota
	MisfireSkip
	MisfireCatchUp
)

const maxCatchUpExecutions = 3

type Engine struct {
	db       Repository
	svcFunc  func() core.TelegramServicer
	router   *core.Router
	perms    *core.Permissions
	executor *core.CommandExecutor
	logger   *zap.Logger

	workers *workers.Manager
	taskMgr *tasks.Manager
	jobsMgr *jobs.Manager

	maxConcurrency int
	sem            chan struct{}
	misfirePolicy  MisfirePolicy

	tasks    map[string]context.CancelFunc
	taskMeta map[string]*periodicTaskMeta
	tasksMu  sync.RWMutex

	activeJobs   map[int64]map[string]context.CancelFunc
	activeJobsMu sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	running bool
	runMu   sync.Mutex
}

type periodicTaskMeta struct {
	Owner     string
	Name      string
	Runs      int64
	Failures  int64
	LastRunAt time.Time
	LastError string
}

// PeriodicTaskSnapshot exposes runtime diagnostics without exposing cancel
// functions or internal scheduler state.
type PeriodicTaskSnapshot struct {
	Owner     string
	Name      string
	Runs      int64
	Failures  int64
	LastRunAt time.Time
	LastError string
}

// PeriodicTaskOptions controls timeout and retry behavior for one periodic
// task execution. MaxAttempts defaults to one; retries are opt-in.
type PeriodicTaskOptions struct {
	Owner       string
	Timeout     time.Duration
	MaxAttempts int
	RetryDelay  time.Duration
}

var _ Service = (*Engine)(nil)

func NewEngine(db Repository, svcFunc func() core.TelegramServicer, router *core.Router, perms *core.Permissions, logger *zap.Logger) *Engine {
	if logger == nil {
		logger = zap.NewNop()
	}
	const defaultConcurrency = 4
	return &Engine{
		db:             db,
		svcFunc:        svcFunc,
		router:         router,
		perms:          perms,
		executor:       core.NewCommandExecutor(logger, nil, 30*time.Second),
		logger:         logger,
		tasks:          make(map[string]context.CancelFunc),
		taskMeta:       make(map[string]*periodicTaskMeta),
		activeJobs:     make(map[int64]map[string]context.CancelFunc),
		maxConcurrency: defaultConcurrency,
		sem:            make(chan struct{}, defaultConcurrency),
		misfirePolicy:  MisfireRunOnce,
	}
}

// SetMaxConcurrency changes the concurrency limit only while the engine is stopped.
// Replacing a live semaphore would split accounting between old and new workers.
func (e *Engine) SetMaxConcurrency(n int) {
	if n <= 0 {
		n = 1
	}
	e.runMu.Lock()
	defer e.runMu.Unlock()
	if e.running {
		return
	}
	e.maxConcurrency = n
	e.sem = make(chan struct{}, n)
}

// SetWorkers configures runtime worker and task managers for job execution.
func (e *Engine) SetWorkers(workers *workers.Manager, taskMgr *tasks.Manager) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.workers = workers
	e.taskMgr = taskMgr
}

// SetJobsManager configures the declarative jobs manager for managed job dispatch.
func (e *Engine) SetJobsManager(jobsMgr *jobs.Manager) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.jobsMgr = jobsMgr
}

func (e *Engine) SetMisfirePolicy(policy MisfirePolicy) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.misfirePolicy = policy
}

func (e *Engine) MisfirePolicy() MisfirePolicy {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	return e.misfirePolicy
}

// IsRunning reports whether the scheduler lifecycle is active.
func (e *Engine) IsRunning() bool {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	return e.running
}

func (e *Engine) SetExecutor(executor *core.CommandExecutor) {
	if executor != nil {
		e.executor = executor
	}
}

func validateActionType(actionType string) error {
	_, err := ParseActionType(actionType)
	return err
}

func (e *Engine) Start(parentCtx context.Context) error {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	if e.running {
		return errors.New("scheduler engine already running")
	}
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	e.ctx, e.cancel = context.WithCancel(parentCtx)
	e.running = true
	e.wg.Add(1)
	go e.runLoop(e.ctx)
	e.logger.Info("scheduler engine started")
	return nil
}

// Ensure Engine implements runtime.Component.
var _ runtime.Component = (*Engine)(nil)

// Name returns component identifier for runtime.Component.
func (e *Engine) Name() string {
	return "scheduler"
}

// Dependencies returns component prerequisites for runtime.Component.
func (e *Engine) Dependencies() []string {
	return []string{"eventbus", "workers"}
}

// Stop gracefully stops the scheduler using the provided context.
func (e *Engine) Stop(ctx context.Context) error {
	return e.StopContext(ctx)
}

// Health evaluates Scheduler engine health.
func (e *Engine) Health(ctx context.Context) runtime.ComponentHealth {
	e.runMu.Lock()
	running := e.running
	e.runMu.Unlock()
	if !running {
		return runtime.ComponentHealth{
			Status:  runtime.HealthDegraded,
			Details: "scheduler engine is not running",
		}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}

func (e *Engine) StopContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.runMu.Lock()
	if !e.running {
		e.runMu.Unlock()
		return nil
	}
	e.running = false
	e.cancel()
	e.runMu.Unlock()

	e.tasksMu.Lock()
	for _, cancelTask := range e.tasks {
		cancelTask()
	}
	e.tasks = make(map[string]context.CancelFunc)
	e.taskMeta = make(map[string]*periodicTaskMeta)
	e.tasksMu.Unlock()

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		e.logger.Info("scheduler engine stopped gracefully")
		return nil
	case <-ctx.Done():
		e.logger.Warn("scheduler engine stop timed out waiting for jobs", zap.Error(ctx.Err()))
		return fmt.Errorf("scheduler engine stop timed out: %w", ctx.Err())
	}
}

func (e *Engine) StopWithTimeout(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return e.StopContext(ctx)
}

// RegisterPeriodicTask registers a lifecycle-bound recurring task.
func (e *Engine) RegisterPeriodicTask(name string, interval time.Duration, task TaskFunc) error {
	return e.RegisterPeriodicTaskWithOptions(name, interval, PeriodicTaskOptions{Owner: "runtime"}, task)
}

// RegisterPeriodicTaskOwned registers a periodic task with an explicit owner
// for diagnostics and future quota enforcement.
func (e *Engine) RegisterPeriodicTaskOwned(owner, name string, interval time.Duration, task TaskFunc) error {
	return e.RegisterPeriodicTaskWithOptions(name, interval, PeriodicTaskOptions{Owner: owner}, task)
}

// RegisterPeriodicTaskWithOptions registers a periodic task with explicit
// ownership, timeout, and retry semantics.
func (e *Engine) RegisterPeriodicTaskWithOptions(name string, interval time.Duration, options PeriodicTaskOptions, task TaskFunc) error {
	owner := strings.TrimSpace(options.Owner)
	if owner == "" {
		owner = "runtime"
	}
	if name == "" {
		return errors.New("task name cannot be empty")
	}
	if interval <= 0 {
		return errors.New("interval must be positive")
	}
	if task == nil {
		return errors.New("task function cannot be nil")
	}
	if options.MaxAttempts < 0 {
		return errors.New("max attempts cannot be negative")
	}
	if options.MaxAttempts == 0 {
		options.MaxAttempts = 1
	}
	if options.RetryDelay < 0 {
		return errors.New("retry delay cannot be negative")
	}

	e.runMu.Lock()
	if !e.running || e.ctx == nil || e.cancel == nil {
		e.runMu.Unlock()
		return errors.New("scheduler engine is not running: call Start() first")
	}
	taskCtx, cancel := context.WithCancel(e.ctx)
	e.tasksMu.Lock()
	if cancelExisting, exists := e.tasks[name]; exists {
		cancelExisting()
	}
	e.tasks[name] = cancel
	e.taskMeta[name] = &periodicTaskMeta{Owner: owner, Name: name}
	e.wg.Add(1)
	e.tasksMu.Unlock()
	e.runMu.Unlock()

	go func() {
		defer e.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-taskCtx.Done():
				return
			case <-ticker.C:
				func() {
					err := runPeriodicTask(taskCtx, task, options)
					e.tasksMu.Lock()
					if meta := e.taskMeta[name]; meta != nil {
						meta.Runs++
						meta.LastRunAt = time.Now().UTC()
						if err != nil {
							meta.Failures++
							meta.LastError = err.Error()
						}
					}
					e.tasksMu.Unlock()
					if err != nil {
						e.logger.Warn("periodic task execution error", zap.String("task", name), zap.Error(err))
					}
				}()
			}
		}
	}()
	return nil
}

func runPeriodicTask(parent context.Context, task TaskFunc, options PeriodicTaskOptions) (err error) {
	maxAttempts := options.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("periodic task panic: %v", recovered)
		}
	}()
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := parent.Err(); err != nil {
			return err
		}
		attemptCtx := parent
		cancel := func() {}
		if options.Timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(parent, options.Timeout)
		}
		err = task(attemptCtx)
		cancel()
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || attempt == maxAttempts {
			return err
		}
		if options.RetryDelay > 0 {
			timer := time.NewTimer(options.RetryDelay)
			select {
			case <-parent.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return parent.Err()
			case <-timer.C:
			}
		}
	}
	return err
}

func (e *Engine) UnregisterPeriodicTask(name string) error {
	e.tasksMu.Lock()
	defer e.tasksMu.Unlock()
	if cancelTask, exists := e.tasks[name]; exists {
		cancelTask()
		delete(e.tasks, name)
		delete(e.taskMeta, name)
		return nil
	}
	return errors.New("task not found")
}

// UnregisterPeriodicTasksByOwner cancels and removes all periodic tasks registered by the specified owner.
func (e *Engine) UnregisterPeriodicTasksByOwner(owner string) int {
	cleanOwner := strings.TrimSpace(owner)
	if cleanOwner == "" {
		return 0
	}
	e.tasksMu.Lock()
	defer e.tasksMu.Unlock()

	count := 0
	for name, meta := range e.taskMeta {
		if meta != nil && (meta.Owner == cleanOwner || meta.Owner == "plugin:"+cleanOwner) {
			if cancelTask, exists := e.tasks[name]; exists {
				cancelTask()
				delete(e.tasks, name)
				delete(e.taskMeta, name)
				count++
			}
		}
	}
	return count
}

// PeriodicTaskSnapshots returns a stable copy of registered periodic task
// diagnostics. It is safe to call while tasks are running.
func (e *Engine) PeriodicTaskSnapshots() []PeriodicTaskSnapshot {
	e.tasksMu.RLock()
	defer e.tasksMu.RUnlock()
	snapshots := make([]PeriodicTaskSnapshot, 0, len(e.taskMeta))
	for _, meta := range e.taskMeta {
		snapshots = append(snapshots, PeriodicTaskSnapshot{
			Owner: meta.Owner, Name: meta.Name, Runs: meta.Runs,
			Failures: meta.Failures, LastRunAt: meta.LastRunAt, LastError: meta.LastError,
		})
	}
	return snapshots
}

func (e *Engine) ScheduleOnce(ctx context.Context, chatID int64, peerType string, accessHash int64, when time.Time, actionType string, payload string, creatorID ...int64) (*ScheduledJob, error) {
	if err := validateActionType(actionType); err != nil {
		return nil, err
	}
	if peerType == "" {
		peerType = "chat"
	}
	var createdBy int64
	if len(creatorID) > 0 {
		createdBy = creatorID[0]
	}
	job := &ScheduledJob{
		ChatID: chatID, PeerType: peerType, AccessHash: accessHash,
		ActionType: actionType, Payload: payload, IntervalSeconds: 0,
		NextRunAt: when, CreatedAt: time.Now().UTC(), CreatedBy: createdBy,
		Status: JobStatusPending, MaxAttempts: 3,
	}
	return e.db.CreateScheduledJob(ctx, job)
}

func (e *Engine) ScheduleRecurring(ctx context.Context, chatID int64, peerType string, accessHash int64, interval time.Duration, actionType string, payload string, creatorID ...int64) (*ScheduledJob, error) {
	if err := validateActionType(actionType); err != nil {
		return nil, err
	}
	if interval < time.Second {
		return nil, fmt.Errorf("%w: recurring interval must be at least 1 second (got %v)", core.ErrInvalidArgs, interval)
	}
	if peerType == "" {
		peerType = "chat"
	}
	var createdBy int64
	if len(creatorID) > 0 {
		createdBy = creatorID[0]
	}
	sec := int64(interval.Seconds())
	job := &ScheduledJob{
		ChatID: chatID, PeerType: peerType, AccessHash: accessHash,
		ActionType: actionType, Payload: payload, IntervalSeconds: sec,
		NextRunAt: time.Now().UTC().Add(interval), CreatedAt: time.Now().UTC(),
		CreatedBy: createdBy, Status: JobStatusPending, MaxAttempts: 3,
	}
	return e.db.CreateScheduledJob(ctx, job)
}

// Cancel removes the durable job first, then cancels every local execution for
// the job. Removing it from the DB prevents it from being reclaimed by another
// worker while the context cancellation stops in-flight Telegram operations.
func (e *Engine) Cancel(ctx context.Context, jobID int64) error {
	if jobID <= 0 {
		return errors.New("invalid scheduled job ID")
	}
	if err := e.db.DeleteScheduledJob(ctx, jobID); err != nil {
		return err
	}
	e.cancelActiveJob(jobID)
	return nil
}

func (e *Engine) List(ctx context.Context, chatID int64) ([]ScheduledJob, error) {
	return e.db.ListScheduledJobs(ctx, chatID)
}

func (e *Engine) JobHistory(ctx context.Context, jobID int64, limit int) ([]JobHistoryEntry, error) {
	return e.db.GetJobHistory(ctx, jobID, limit)
}

func (e *Engine) registerActiveJob(jobID int64, claimToken string, cancel context.CancelFunc) {
	e.activeJobsMu.Lock()
	defer e.activeJobsMu.Unlock()
	workers := e.activeJobs[jobID]
	if workers == nil {
		workers = make(map[string]context.CancelFunc)
		e.activeJobs[jobID] = workers
	}
	workers[claimToken] = cancel
}

func (e *Engine) unregisterActiveJob(jobID int64, claimToken string) {
	e.activeJobsMu.Lock()
	defer e.activeJobsMu.Unlock()
	workers := e.activeJobs[jobID]
	if workers == nil {
		return
	}
	delete(workers, claimToken)
	if len(workers) == 0 {
		delete(e.activeJobs, jobID)
	}
}

func (e *Engine) cancelActiveJob(jobID int64) {
	e.activeJobsMu.Lock()
	workers := e.activeJobs[jobID]
	for _, cancel := range workers {
		cancel()
	}
	e.activeJobsMu.Unlock()
}

func (e *Engine) runLoop(ctx context.Context) {
	defer e.wg.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			e.processDueJobs(ctx, now)
		}
	}
}

func (e *Engine) processDueJobs(ctx context.Context, now time.Time) {
	// P1-01: Do not claim jobs before Telegram service is ready.
	if e.svcFunc != nil && e.svcFunc() == nil {
		return
	}
	availableSlots := cap(e.sem) - len(e.sem)
	if e.workers != nil {
		if schedPool, ok := e.workers.Get(workers.PoolScheduler); ok {
			stats := schedPool.Stats()
			// Durable jobs should only be claimed when a worker can start them.
			// Queue capacity is not execution capacity: claiming queued work starts
			// its lease before it can run and can cause avoidable lease expiry.
			avail := stats.Concurrency - stats.Busy
			if avail < availableSlots {
				availableSlots = avail
			}
		}
	}
	if availableSlots <= 0 {
		return
	}
	claimBatch := availableSlots
	if claimBatch > 10 {
		claimBatch = 10
	}
	claimedJobs, err := e.db.ClaimDueScheduledJobs(ctx, now, claimBatch, 90*time.Second)
	if err != nil {
		e.logger.Error("failed to claim due scheduled jobs", zap.Error(err))
		return
	}
	for _, job := range claimedJobs {
		j := job
		if e.workers != nil {
			select {
			case e.sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			var reservationOnce sync.Once
			releaseReservation := func() {
				reservationOnce.Do(func() { <-e.sem })
			}
			taskID := fmt.Sprintf("sched-%d-%s", j.ID, j.ClaimToken)
			jobCtx, cancel := context.WithCancel(e.ctx)
			context.AfterFunc(jobCtx, releaseReservation)
			e.registerActiveJob(j.ID, j.ClaimToken, cancel)
			taskName := fmt.Sprintf("%s-%d", j.ActionType, j.ID)

			task := tasks.Task{
				ID:        taskID,
				Owner:     "scheduler",
				Name:      taskName,
				Timeout:   90 * time.Second,
				CreatedAt: time.Now().UTC(),
				Run: func(taskCtx context.Context) error {
					defer func() {
						releaseReservation()
						e.unregisterActiveJob(j.ID, j.ClaimToken)
						cancel()
					}()
					e.executeJob(taskCtx, j, cancel)
					return nil
				},
			}

			if err := e.workers.Submit(ctx, workers.PoolScheduler, task); err != nil {
				releaseReservation()
				e.logger.Error("failed to submit scheduled job to worker pool", zap.Int64("job_id", j.ID), zap.Error(err))
				e.unregisterActiveJob(j.ID, j.ClaimToken)
				cancel()
			}
			continue
		}

		select {
		case e.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		e.wg.Add(1)
		go func(targetJob ScheduledJob) {
			defer func() { <-e.sem; e.wg.Done() }()
			jobCtx, cancel := context.WithCancel(e.ctx)
			e.registerActiveJob(targetJob.ID, targetJob.ClaimToken, cancel)
			defer func() {
				e.unregisterActiveJob(targetJob.ID, targetJob.ClaimToken)
				cancel()
			}()
			e.executeJob(jobCtx, targetJob, cancel)
		}(j)
	}
}

// executeJob deliberately uses an at-least-once external execution model.
// Database fencing prevents stale workers from mutating durable state, but it
// cannot roll back a Telegram side effect that succeeded immediately before a
// worker crash or lease loss. Callers must therefore treat scheduled actions
// as potentially duplicated across crash recovery.
func (e *Engine) executeJob(ctx context.Context, job ScheduledJob, cancel context.CancelFunc) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Error("scheduled job execution panicked", zap.Int64("job_id", job.ID), zap.Any("panic", r))
			stateCtx, stateCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer stateCancel()
			_ = e.db.FailScheduledJob(stateCtx, job.ID, job.ClaimToken, fmt.Sprintf("panic: %v", r), 0, 10*time.Second, false, time.Now().UTC())
		}
	}()

	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				if err := e.db.RenewJobLease(ctx, job.ID, job.ClaimToken, 90*time.Second, t.UTC()); err != nil {
					e.logger.Warn("heartbeat lease renewal failed or lease lost, cancelling execution context", zap.Int64("job_id", job.ID), zap.Error(err))
					cancel()
					return
				}
			}
		}
	}()

	startTime := time.Now()
	runs := 1
	policy := e.MisfirePolicy()
	if job.IntervalSeconds > 0 {
		overdue := time.Since(job.NextRunAt)
		if overdue > time.Minute {
			switch policy {
			case MisfireSkip:
				e.logger.Warn("recurring scheduled job misfired: skipping execution", zap.Int64("job_id", job.ID), zap.Time("next_run_at", job.NextRunAt))
				stateCtx, stateCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer stateCancel()
				if err := e.db.CompleteScheduledJob(stateCtx, job.ID, job.ClaimToken, 0, time.Now().UTC()); err != nil && !errors.Is(err, ErrJobLeaseLost) {
					e.logger.Warn("failed to advance skipped recurring job", zap.Int64("job_id", job.ID), zap.Error(err))
				}
				return
			case MisfireCatchUp:
				interval := time.Duration(job.IntervalSeconds) * time.Second
				missed := int(overdue/interval) + 1
				if missed > maxCatchUpExecutions {
					missed = maxCatchUpExecutions
				}
				runs = missed
			case MisfireRunOnce:
				runs = 1
			}
		}
	}

	var execErr error
	for r := 0; r < runs; r++ {
		select {
		case <-ctx.Done():
			execErr = ctx.Err()
			break
		default:
		}

		switch job.ActionType {
		case ActionMessage:
			execErr = e.executeSendMessage(ctx, job)
		case ActionCommand:
			execErr = e.executeCommand(ctx, job)
		case ActionJob:
			execErr = e.executeManagedJob(ctx, job)
		default:
			execErr = fmt.Errorf("unknown scheduled job action type: %s", job.ActionType)
		}

		if execErr != nil {
			break
		}
	}

	now := time.Now().UTC()
	durationMs := now.Sub(startTime).Milliseconds()
	if execErr == nil {
		stateCtx, stateCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stateCancel()
		if err := e.db.CompleteScheduledJob(stateCtx, job.ID, job.ClaimToken, durationMs, now); err != nil {
			if errors.Is(err, ErrJobLeaseLost) {
				e.logger.Warn("scheduled job lease lost or cancelled before completion", zap.Int64("job_id", job.ID))
			} else {
				e.logger.Error("failed to complete scheduled job", zap.Int64("job_id", job.ID), zap.Error(err))
			}
		}
		return
	}

	// A shutdown or explicit cancellation deliberately leaves the durable job
	// alone if the DB row was already removed; an expired lease can then recover
	// unfinished work after a restart without turning cancellation into a retry.
	if errors.Is(execErr, context.Canceled) || errors.Is(execErr, context.DeadlineExceeded) {
		e.logger.Info("scheduled job execution cancelled", zap.Int64("job_id", job.ID), zap.Error(execErr))
		return
	}

	isPermanent := core.IsPermanentError(execErr)
	var retryDelay time.Duration
	if !isPermanent {
		attempt := job.AttemptCount
		if attempt < 1 {
			attempt = 1
		}
		backoffMultiplier := 1 << (attempt - 1)
		if backoffMultiplier > 30 {
			backoffMultiplier = 30
		}
		retryDelay = time.Duration(10*backoffMultiplier) * time.Second
	}
	stateCtx, stateCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stateCancel()
	if err := e.db.FailScheduledJob(stateCtx, job.ID, job.ClaimToken, execErr.Error(), durationMs, retryDelay, isPermanent, now); err != nil {
		if errors.Is(err, ErrJobLeaseLost) {
			e.logger.Warn("scheduled job lease lost or claimed by another worker on fail", zap.Int64("job_id", job.ID))
		} else {
			e.logger.Error("failed to record scheduled job failure", zap.Int64("job_id", job.ID), zap.Error(err))
		}
	}
}

func (e *Engine) executeSendMessage(ctx context.Context, job ScheduledJob) error {
	if e.svcFunc == nil {
		return errors.New("cannot execute scheduled message: servicer function is nil")
	}
	svc := e.svcFunc()
	if svc == nil {
		return errors.New("cannot execute scheduled message: servicer is nil")
	}
	peer := reconstructInputPeer(job.PeerType, job.ChatID, job.AccessHash)
	text := job.Payload
	if job.IntervalSeconds == 0 && time.Since(job.NextRunAt) > time.Minute {
		text = "⏰ <b>Reminder</b> (<i>delayed, bot was offline</i>):\n" + job.Payload
	}
	if _, err := svc.SendMessage(ctx, peer, text); err != nil {
		e.logger.Warn("failed to send scheduled message", zap.Int64("chat_id", job.ChatID), zap.Error(err))
		return err
	}
	return nil
}

func (e *Engine) executeCommand(ctx context.Context, job ScheduledJob) error {
	if e.router == nil {
		return errors.New("cannot execute scheduled command: router is nil")
	}
	if e.svcFunc == nil {
		return errors.New("cannot execute scheduled command: servicer function is nil")
	}
	svc := e.svcFunc()
	if svc == nil {
		return errors.New("cannot execute scheduled command: servicer is nil")
	}
	parsed, isCmd, err := e.router.Parse(job.Payload)
	if err != nil {
		return fmt.Errorf("%w: %v", core.ErrInvalidArgs, err)
	}
	if !isCmd {
		return fmt.Errorf("scheduled command payload is not a command: %s", job.Payload)
	}
	cmd, exists := e.router.Find(parsed.Name)
	if !exists {
		return fmt.Errorf("scheduled command not found in router: %s", parsed.Name)
	}
	peer := reconstructInputPeer(job.PeerType, job.ChatID, job.AccessHash)
	callerID := job.CreatedBy
	var principal *core.Principal
	if e.perms != nil {
		principal, _ = e.perms.Resolve(ctx, callerID)
	}
	// P0-03/P0-04: Use explicit ExecutionScheduled source without synthetic
	// Message{ID:0,IsOutgoing:true}. This ensures Follow-Up policy uses
	// FilterMiddlewareForSource (no outgoing bypass) and EditOrReply falls
	// back to Reply instead of attempting to edit a non-existent message.
	exec := core.CommandExecution{
		Ctx:           ctx,
		Source:        core.ExecutionScheduled,
		Command:       parsed.Name,
		Args:          parsed.Args,
		RawArgs:       parsed.RawArgs,
		Principal:     principal,
		Perms:         e.perms,
		Chat:          &core.Chat{ID: job.ChatID, Type: job.PeerType},
		Sender:        &core.User{ID: callerID},
		PeerID:        peer,
		CorrelationID: fmt.Sprintf("sched-%d-%d", job.ID, time.Now().UnixMilli()),
	}
	return e.executor.ExecuteExecution(exec, cmd, svc)
}

func (e *Engine) executeManagedJob(ctx context.Context, job ScheduledJob) error {
	if e.jobsMgr == nil {
		return errors.New("jobs manager not configured on scheduler engine")
	}
	jobID := strings.TrimSpace(job.Payload)
	if jobID == "" {
		return errors.New("empty job id in scheduled managed job payload")
	}
	return e.jobsMgr.Trigger(ctx, jobID)
}

// ScheduleManagedJob schedules a declarative job from jobs.Manager to run at when, optionally recurring.
func (e *Engine) ScheduleManagedJob(ctx context.Context, jobID string, when time.Time, interval time.Duration) (*ScheduledJob, error) {
	if interval <= 0 {
		return e.ScheduleOnce(ctx, 0, "internal", 0, when, ActionJob, jobID)
	}
	return e.ScheduleRecurring(ctx, 0, "internal", 0, interval, ActionJob, jobID)
}

func reconstructInputPeer(peerType string, chatID int64, accessHash int64) tg.InputPeerClass {
	switch peerType {
	case "self":
		return &tg.InputPeerSelf{}
	case "user":
		return &tg.InputPeerUser{UserID: chatID, AccessHash: accessHash}
	case "channel", "supergroup":
		return &tg.InputPeerChannel{ChannelID: chatID, AccessHash: accessHash}
	default:
		return &tg.InputPeerChat{ChatID: chatID}
	}
}

func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errors.New("duration cannot be empty")
	}
	s = strings.ReplaceAll(s, "minutes", "m")
	s = strings.ReplaceAll(s, "minute", "m")
	s = strings.ReplaceAll(s, "mins", "m")
	s = strings.ReplaceAll(s, "min", "m")
	s = strings.ReplaceAll(s, "hours", "h")
	s = strings.ReplaceAll(s, "hour", "h")
	s = strings.ReplaceAll(s, "seconds", "s")
	s = strings.ReplaceAll(s, "second", "s")
	s = strings.ReplaceAll(s, "secs", "s")
	s = strings.ReplaceAll(s, "sec", "s")
	s = strings.ReplaceAll(s, "days", "d")
	s = strings.ReplaceAll(s, "day", "d")
	s = strings.ReplaceAll(s, " ", "")
	if strings.Contains(s, "d") {
		parts := strings.SplitN(s, "d", 2)
		days, err := strconv.Atoi(parts[0])
		if err != nil || days < 0 {
			return 0, fmt.Errorf("invalid day duration: %s", s)
		}
		total := time.Duration(days) * 24 * time.Hour
		if len(parts) > 1 && parts[1] != "" {
			rem, err := time.ParseDuration(parts[1])
			if err != nil {
				return 0, err
			}
			total += rem
		}
		return total, nil
	}
	return time.ParseDuration(s)
}
