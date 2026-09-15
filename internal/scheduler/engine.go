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
	"go.uber.org/zap"
)

type MisfirePolicy int

const (
	MisfireRunOnce MisfirePolicy = iota
	MisfireSkip
	MisfireCatchUp
)

const (
	// actionTimeout bounds one scheduled action attempt inside TaskEngine.
	actionTimeout = 90 * time.Second
	// schedulerClaimLease covers a full attempt lifetime (execution timeout
	// plus commit slack) so the claim lease can never expire mid-execution.
	// The per-execution heartbeat is gone: execution fits inside the lease
	// by construction, and retries belong to the JobManager attempt protocol.
	schedulerClaimLease = 3 * time.Minute
	// misfireThreshold bounds staleness: a due slot older than this is a
	// misfire (bot was offline) rather than normal scheduling jitter.
	misfireThreshold = time.Minute
)

// trackedClaim follows one claimed row until its occurrence settles durably.
// Settlement is observed by polling the durable occurrence (timing-layer
// reconciliation), never by ad-hoc per-execution goroutines.
type trackedClaim struct {
	jobID        int64
	claimToken   string
	occurrenceID string
}

type Engine struct {
	db       Repository
	svcFunc  func() core.TelegramServicer
	router   *core.Router
	perms    *core.Permissions
	executor *core.CommandExecutor
	logger   *zap.Logger

	tasks   tasks.Client
	jobsMgr *jobs.Manager

	claimBatchSize int
	misfirePolicy  MisfirePolicy

	wakeChan chan struct{}

	claimMu   sync.Mutex
	claimWG   sync.WaitGroup
	quiescing bool

	// claimsMu guards tracked in-flight claims (row ID -> claim).
	claimsMu sync.Mutex
	claims   map[int64]*trackedClaim

	periodic *periodicCoordinator

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	running bool
	runMu   sync.Mutex
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
	const defaultBatchSize = 4
	engine := &Engine{
		db:             db,
		svcFunc:        svcFunc,
		router:         router,
		perms:          perms,
		executor:       core.NewCommandExecutor(logger, nil, 30*time.Second),
		logger:         logger,
		claimBatchSize: defaultBatchSize,
		misfirePolicy:  MisfireRunOnce,
		wakeChan:       make(chan struct{}, 1),
	}
	engine.periodic = newPeriodicCoordinator(logger)
	engine.claims = make(map[int64]*trackedClaim)
	return engine
}

func (e *Engine) notifyWake() {
	if e == nil || e.wakeChan == nil {
		return
	}
	select {
	case e.wakeChan <- struct{}{}:
	default:
	}
}

// SetMaxConcurrency is deprecated. Physical scheduler concurrency is governed
// by the worker manager pool (PoolScheduler). In standalone/test mode, this configures the claim batch size.
func (e *Engine) SetMaxConcurrency(n int) {
	if n <= 0 {
		n = 1
	}
	e.runMu.Lock()
	defer e.runMu.Unlock()
	if e.running {
		return
	}
	e.claimBatchSize = n
}

// SetTasks configures the sole execution authority for due occurrences.
func (e *Engine) SetTasks(client tasks.Client) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.tasks = client
}

// SetJobsManager configures the declarative jobs manager for managed job dispatch.
func (e *Engine) SetJobsManager(jobsMgr *jobs.Manager) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.jobsMgr = jobsMgr
	if jobsMgr == nil {
		return
	}
	if err := jobsMgr.RegisterHandler("scheduler.action", e.runScheduledAction); err != nil {
		e.logger.Warn("register scheduler action handler", zap.Error(err))
	}
	if err := jobsMgr.RegisterHandler(periodicHandlerType, e.runPeriodicAction); err != nil {
		e.logger.Warn("register periodic action handler", zap.Error(err))
	}
	if e.periodic != nil {
		e.periodic.SetJobsManager(jobsMgr)
	}
}

// runPeriodicAction dispatches one periodic attempt to the currently
// registered function. Timing, retry, and durability belong to the
// coordinator loop and JobManager; this only bridges into the TaskFunc.
func (e *Engine) runPeriodicAction(ctx context.Context, definition jobs.JobDefinition) error {
	if e.periodic == nil {
		return errors.New("periodic coordinator is not configured")
	}
	return e.periodic.runTaskFunc(ctx, definition.ID)
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
	if e.periodic != nil {
		if err := e.periodic.Start(e.ctx); err != nil {
			e.cancel()
			return fmt.Errorf("start periodic coordinator: %w", err)
		}
	}
	e.claimMu.Lock()
	e.quiescing = false
	e.claimMu.Unlock()
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
	return []string{"jobs", "taskengine"}
}

// Stop gracefully stops the scheduler using the provided context.
func (e *Engine) Stop(ctx context.Context) error {
	return e.StopContext(ctx)
}

func (e *Engine) beginClaimBatch() bool {
	e.claimMu.Lock()
	defer e.claimMu.Unlock()
	if e.quiescing {
		return false
	}
	e.claimWG.Add(1)
	return true
}

func (e *Engine) endClaimBatch() {
	e.claimWG.Done()
}

// Quiesce stops new durable claims and waits for any claim->submit handoff that
// already started. This runs before TaskEngine quiesces via the runtime DAG,
// preventing fresh leases from being claimed after worker admission closes.
func (e *Engine) Quiesce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.claimMu.Lock()
	e.quiescing = true
	e.claimMu.Unlock()
	e.notifyWake()

	done := make(chan struct{})
	go func() {
		e.claimWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
	if err := e.Quiesce(ctx); err != nil {
		return err
	}
	e.runMu.Lock()
	if !e.running {
		e.runMu.Unlock()
		return nil
	}
	e.running = false
	e.cancel()
	e.runMu.Unlock()

	if e.periodic != nil {
		if err := e.periodic.Stop(ctx); err != nil {
			return fmt.Errorf("stop periodic coordinator: %w", err)
		}
	}

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
	if e.periodic == nil {
		return errors.New("periodic coordinator is not configured")
	}
	return e.periodic.Register(name, interval, options, task)
}

func (e *Engine) UnregisterPeriodicTask(name string) error {
	if e.periodic == nil {
		return errors.New("periodic coordinator is not configured")
	}
	return e.periodic.Unregister(name)
}

// UnregisterPeriodicTaskOwned unregisters a specific periodic task registered with an explicit owner.
func (e *Engine) UnregisterPeriodicTaskOwned(owner, name string) error {
	if e.periodic == nil {
		return errors.New("periodic coordinator is not configured")
	}
	return e.periodic.UnregisterOwned(owner, name)
}

// UnregisterPeriodicTasksByOwner cancels and removes all periodic tasks registered by the specified owner.
func (e *Engine) UnregisterPeriodicTasksByOwner(owner string) int {
	if e.periodic == nil {
		return 0
	}
	return e.periodic.UnregisterByOwner(owner)
}

// PeriodicTaskSnapshots returns a stable copy of registered periodic task
// diagnostics. It is safe to call while tasks are running.
func (e *Engine) PeriodicTaskSnapshots() []PeriodicTaskSnapshot {
	if e.periodic == nil {
		return nil
	}
	return e.periodic.Snapshots()
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
	res, err := e.db.CreateScheduledJob(ctx, job)
	if err != nil {
		return nil, err
	}
	if err := e.registerScheduledDefinition(res); err != nil {
		_ = e.db.DeleteScheduledJob(ctx, res.ID)
		return nil, err
	}
	e.notifyWake()
	return res, nil
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
	res, err := e.db.CreateScheduledJob(ctx, job)
	if err != nil {
		return nil, err
	}
	if err := e.registerScheduledDefinition(res); err != nil {
		_ = e.db.DeleteScheduledJob(ctx, res.ID)
		return nil, err
	}
	e.notifyWake()
	return res, nil
}

func scheduledDefinitionID(jobID int64) string { return fmt.Sprintf("scheduler:job:%d", jobID) }

func (e *Engine) registerScheduledDefinition(job *ScheduledJob) error {
	if job == nil || e.jobsMgr == nil {
		return errors.New("scheduler jobs manager is not configured")
	}
	return e.jobsMgr.Register(jobs.JobDefinition{
		ID:          scheduledDefinitionID(job.ID),
		ScopeOwner:  scheduledTaskScope(job.ID),
		QuotaOwner:  "scheduler",
		HandlerType: "scheduler.action",
		Pool:        "scheduler",
		Class:       string(tasks.PriorityMaintenance),
		Timeout:     actionTimeout,
		// No scheduler-level retries: a failed attempt is retried by the
		// JobManager attempt protocol, and the row only advances when the
		// occurrence settles durably (reconcileSettledClaims).
		RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 1},
		Enabled:     true,
	})
}

// Cancel removes the durable job first, then cancels every local execution for
// the job. Removing it from the DB prevents it from being reclaimed by another
// worker while the context cancellation stops in-flight Telegram operations.
// A tracked in-flight occurrence is also cancelled durably so the JobManager
// retry driver can never resurrect it.
func (e *Engine) Cancel(ctx context.Context, jobID int64) error {
	if jobID <= 0 {
		return errors.New("invalid scheduled job ID")
	}
	if err := e.db.DeleteScheduledJob(ctx, jobID); err != nil {
		return err
	}
	if e.tasks != nil {
		e.tasks.CancelScope(tasks.ScopeIdentity{Owner: scheduledTaskScope(jobID), Generation: 1}, tasks.CauseUserCancel)
	}
	e.claimsMu.Lock()
	claim, tracked := e.claims[jobID]
	if tracked {
		delete(e.claims, jobID)
	}
	e.claimsMu.Unlock()
	if tracked && e.jobsMgr != nil {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = e.jobsMgr.CancelOccurrence(cctx, claim.occurrenceID, "scheduled job cancelled")
	}
	e.notifyWake()
	return nil
}

func (e *Engine) List(ctx context.Context, chatID int64) ([]ScheduledJob, error) {
	return e.db.ListScheduledJobs(ctx, chatID)
}

func (e *Engine) JobHistory(ctx context.Context, jobID int64, limit int) ([]JobHistoryEntry, error) {
	return e.db.GetJobHistory(ctx, jobID, limit)
}

func scheduledTaskScope(jobID int64) string {
	return fmt.Sprintf("scheduler:job:%d", jobID)
}

func (e *Engine) runLoop(ctx context.Context) {
	defer e.wg.Done()

	// idleHeartbeat ensures periodic check even if an external DB change occurred without notifyWake.
	const idleHeartbeat = 60 * time.Second

	timer := time.NewTimer(idleHeartbeat)
	defer timer.Stop()

	for {
		now := time.Now()
		var nextDelay time.Duration

		// Timing-layer reconciliation first: advance rows whose occurrences
		// settled durably since the last pass (including across restarts).
		e.reconcileSettledClaims(ctx)

		earliest, found, err := e.db.GetEarliestDueTime(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return
			}
			e.logger.Error("failed to query earliest due scheduled job", zap.Error(err))
			nextDelay = 1 * time.Second
		} else if !found {
			nextDelay = idleHeartbeat
		} else {
			if earliest.Before(now) || earliest.Equal(now) {
				nextDelay = 0
			} else {
				nextDelay = earliest.Sub(now)
				if nextDelay > idleHeartbeat {
					nextDelay = idleHeartbeat
				}
			}
		}

		if nextDelay <= 0 {
			claimed := e.processDueJobs(ctx, now)
			if claimed == 0 {
				// Due jobs exist but could not be claimed (e.g. workers busy or Telegram not ready).
				// Sleep a short backoff to prevent tight CPU spin.
				nextDelay = 250 * time.Millisecond
			} else {
				// Loop immediately to re-check for any more due jobs or compute the new next delay
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
		}

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		// While claims are in flight, settlement must be observed promptly:
		// cap the sleep so reconciliation cannot starve behind the timer.
		if nextDelay > time.Second && e.trackedClaimCount() > 0 {
			nextDelay = time.Second
		}
		timer.Reset(nextDelay)

		select {
		case <-ctx.Done():
			return
		case <-e.wakeChan:
			// Recalculate deadline immediately
		case fireTime := <-timer.C:
			e.processDueJobs(ctx, fireTime)
		}
	}
}

func (e *Engine) processDueJobs(ctx context.Context, now time.Time) int {
	if !e.beginClaimBatch() {
		return 0
	}
	defer e.endClaimBatch()

	// Do not claim jobs before Telegram service is ready.
	if e.svcFunc != nil && e.svcFunc() == nil {
		return 0
	}

	claimBatch := e.claimBatchSize
	if claimBatch <= 0 {
		claimBatch = 1
	}
	if claimBatch > 10 {
		claimBatch = 10
	}

	if e.jobsMgr == nil {
		e.logger.Error("scheduler jobs manager is not configured")
		return 0
	}

	claimedJobs, err := e.db.ClaimDueScheduledJobs(ctx, now, claimBatch, schedulerClaimLease)
	if err != nil {
		e.logger.Error("failed to claim due scheduled jobs", zap.Error(err))
		return 0
	}

	for _, job := range claimedJobs {
		j := job
		// Stable occurrence key per (row, due slot): a lease-expiry reclaim
		// of the same slot collapses onto the same logical occurrence, and
		// the lease single-flights it instead of duplicating execution.
		occurrenceKey := fmt.Sprintf("sched:%d:%d", j.ID, j.NextRunAt.UTC().UnixNano())
		_, occurrenceID, submitErr := e.jobsMgr.SubmitOccurrence(e.ctx, scheduledDefinitionID(j.ID), occurrenceKey)
		if submitErr != nil {
			e.onSubmitRejected(ctx, j, occurrenceKey, submitErr)
			continue
		}
		e.trackClaim(j.ID, j.ClaimToken, occurrenceID)
	}
	return len(claimedJobs)
}

// onSubmitRejected handles a claim whose occurrence could not be admitted.
// A still-active occurrence (reclaim of a live slot) only needs its lease
// extended; anything else re-pends the row for a later timing pass.
func (e *Engine) onSubmitRejected(ctx context.Context, job ScheduledJob, occurrenceKey string, submitErr error) {
	stateCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if occ, oerr := e.jobsMgr.OccurrenceByKey(stateCtx, occurrenceKey); oerr == nil && occ != nil {
		switch occ.State {
		case jobs.OccurrenceReady, jobs.OccurrenceDispatched:
			// Live occurrence holds the slot: extend the lease one-shot so
			// the row is not reclaimed while the attempt runs. No heartbeat
			// goroutine; the attempt fits inside the renewed lease.
			if rerr := e.db.RenewJobLease(stateCtx, job.ID, job.ClaimToken, schedulerClaimLease, time.Now().UTC()); rerr != nil && !errors.Is(rerr, ErrJobLeaseLost) {
				e.logger.Error("failed to renew lease for live scheduled occurrence", zap.Int64("job_id", job.ID), zap.Error(rerr))
			} else {
				e.trackClaim(job.ID, job.ClaimToken, occ.ID)
			}
			return
		}
	}
	e.logger.Error("failed to admit scheduled occurrence", zap.Int64("job_id", job.ID), zap.Error(submitErr))
	if failErr := e.db.FailScheduledJob(stateCtx, job.ID, job.ClaimToken, submitErr.Error(), 0, time.Second, false, time.Now().UTC()); failErr != nil && !errors.Is(failErr, ErrJobLeaseLost) {
		e.logger.Error("failed to release rejected scheduled claim", zap.Int64("job_id", job.ID), zap.Error(failErr))
	}
}

func (e *Engine) trackClaim(jobID int64, claimToken, occurrenceID string) {
	e.claimsMu.Lock()
	defer e.claimsMu.Unlock()
	if e.claims == nil {
		e.claims = make(map[int64]*trackedClaim)
	}
	e.claims[jobID] = &trackedClaim{jobID: jobID, claimToken: claimToken, occurrenceID: occurrenceID}
}

func (e *Engine) untrackClaim(jobID int64) {
	e.claimsMu.Lock()
	defer e.claimsMu.Unlock()
	delete(e.claims, jobID)
}

// trackedClaimCount reports in-flight claims whose settlement the timing
// loop must observe promptly (bounds runLoop sleep while any exist).
func (e *Engine) trackedClaimCount() int {
	e.claimsMu.Lock()
	defer e.claimsMu.Unlock()
	return len(e.claims)
}

// reconcileSettledClaims advances schedule rows whose occurrences reached a
// durable terminal state. This is the timing layer's only completion path:
// attempts, retries, and commits belong to JobManager/TaskEngine. It runs on
// every runLoop pass, so no per-claim goroutine ever exists.
func (e *Engine) reconcileSettledClaims(ctx context.Context) {
	e.claimsMu.Lock()
	pending := make([]*trackedClaim, 0, len(e.claims))
	for _, c := range e.claims {
		pending = append(pending, c)
	}
	e.claimsMu.Unlock()
	if len(pending) == 0 {
		return
	}
	for _, c := range pending {
		if ctx.Err() != nil {
			return
		}
		e.reconcileClaim(ctx, c)
	}
}

func (e *Engine) reconcileClaim(ctx context.Context, c *trackedClaim) {
	stateCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	row, err := e.db.GetScheduledJob(stateCtx, c.jobID)
	if err != nil || row == nil {
		// Row deleted (Cancel path): nothing left to advance.
		e.untrackClaim(c.jobID)
		return
	}
	if row.ClaimToken != c.claimToken {
		// Superseded claim (re-claimed or reset elsewhere).
		e.untrackClaim(c.jobID)
		return
	}
	occ, err := e.jobsMgr.GetOccurrence(stateCtx, c.occurrenceID)
	if err != nil || occ == nil {
		return // Not materialized yet or store hiccup; keep tracking.
	}
	now := time.Now().UTC()
	switch occ.State {
	case jobs.OccurrenceCompleted:
		if cerr := e.db.CompleteScheduledJob(stateCtx, c.jobID, c.claimToken, occurrenceDurationMs(stateCtx, e.jobsMgr, c.occurrenceID), now); cerr != nil && !errors.Is(cerr, ErrJobLeaseLost) {
			e.logger.Error("failed to complete scheduled job", zap.Int64("job_id", c.jobID), zap.Error(cerr))
			return
		}
		e.pruneSettledOccurrences(c.jobID)
		e.untrackClaim(c.jobID)
		e.notifyWake()
	case jobs.OccurrenceFailed:
		if ferr := e.db.FailScheduledJob(stateCtx, c.jobID, c.claimToken, occurrenceError(stateCtx, e.jobsMgr, c.occurrenceID), 0, 0, true, now); ferr != nil && !errors.Is(ferr, ErrJobLeaseLost) {
			e.logger.Error("failed to record scheduled job failure", zap.Int64("job_id", c.jobID), zap.Error(ferr))
			return
		}
		e.pruneSettledOccurrences(c.jobID)
		e.untrackClaim(c.jobID)
		e.notifyWake()
	case jobs.OccurrenceCancelled:
		// Cancelled attempts must not retry: advance/delete the row without
		// recording a failure.
		if cerr := e.db.CompleteScheduledJob(stateCtx, c.jobID, c.claimToken, 0, now); cerr != nil && !errors.Is(cerr, ErrJobLeaseLost) {
			e.logger.Error("failed to advance cancelled scheduled job", zap.Int64("job_id", c.jobID), zap.Error(cerr))
			return
		}
		e.pruneSettledOccurrences(c.jobID)
		e.untrackClaim(c.jobID)
		e.notifyWake()
	default:
		// Ready/dispatched: attempts still running or retrying under the JobManager.
	}
}

// pruneSettledOccurrences bounds durable growth for one schedule definition:
// rows older than a day are removed best-effort. User-facing history lives in
// the scheduler history table, which settling already appended.
func (e *Engine) pruneSettledOccurrences(jobID int64) {
	if e.jobsMgr == nil {
		return
	}
	pctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = e.jobsMgr.PruneOccurrences(pctx, scheduledDefinitionID(jobID), time.Now().UTC().Add(-24*time.Hour), 500)
}

func occurrenceDurationMs(ctx context.Context, jobsMgr *jobs.Manager, occurrenceID string) int64 {
	if jobsMgr == nil {
		return 0
	}
	latest, err := jobsMgr.LatestAttempt(ctx, occurrenceID)
	if err != nil || latest == nil || latest.FinishedAt.IsZero() || latest.StartedAt.IsZero() {
		return 0
	}
	return latest.FinishedAt.Sub(latest.StartedAt).Milliseconds()
}

func occurrenceError(ctx context.Context, jobsMgr *jobs.Manager, occurrenceID string) string {
	if jobsMgr == nil {
		return "scheduled occurrence failed"
	}
	latest, err := jobsMgr.LatestAttempt(ctx, occurrenceID)
	if err != nil || latest == nil || latest.Error == "" {
		return "scheduled occurrence failed"
	}
	return latest.Error
}

// runScheduledAction is the JobManager handler ("scheduler.action") for one
// scheduled occurrence. It is timing-agnostic and owns no retry, lease, or
// completion state: it performs exactly one action and reports its outcome.
// Attempts, retries, lease fencing, and durable commits belong to
// JobManager/TaskEngine; row advancement happens in reconcileSettledClaims
// once the occurrence settles durably.
//
// Execution is at-least-once: database fencing stops stale workers from
// mutating durable state, but a Telegram side effect that succeeded just
// before a crash cannot be rolled back. A retry after such a crash may
// duplicate the message or command.
func (e *Engine) runScheduledAction(ctx context.Context, definition jobs.JobDefinition) error {
	jobIDText := strings.TrimPrefix(definition.ID, "scheduler:job:")
	jobID, err := strconv.ParseInt(jobIDText, 10, 64)
	if err != nil || jobID <= 0 {
		return fmt.Errorf("invalid scheduler job definition: %s", definition.ID)
	}
	job, err := e.db.GetScheduledJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job == nil || job.ClaimToken == "" {
		return errors.New("scheduled job is no longer claimed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.shouldSkipMisfire(*job) {
		e.logger.Warn("recurring scheduled job misfired: skipping execution", zap.Int64("job_id", job.ID), zap.Time("next_run_at", job.NextRunAt))
		stateCtx, stateCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stateCancel()
		if cerr := e.db.CompleteScheduledJob(stateCtx, job.ID, job.ClaimToken, 0, time.Now().UTC()); cerr != nil && !errors.Is(cerr, ErrJobLeaseLost) {
			e.logger.Warn("failed to advance skipped recurring job", zap.Int64("job_id", job.ID), zap.Error(cerr))
		}
		return nil
	}
	switch job.ActionType {
	case ActionMessage:
		return e.executeSendMessage(ctx, *job)
	case ActionCommand:
		return e.executeCommand(ctx, *job)
	case ActionJob:
		return e.executeManagedJob(ctx, *job)
	default:
		return fmt.Errorf("unknown scheduled job action type: %s", job.ActionType)
	}
}

// shouldSkipMisfire reports whether an overdue recurring slot must be skipped
// without execution. Catch-up is a single compensating run: missed slots
// collapse into the current occurrence instead of multiplying executions.
func (e *Engine) shouldSkipMisfire(job ScheduledJob) bool {
	if job.IntervalSeconds <= 0 {
		return false
	}
	if time.Since(job.NextRunAt) <= misfireThreshold {
		return false
	}
	return e.MisfirePolicy() == MisfireSkip
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
	if err := ctx.Err(); err != nil {
		return err
	}
	// Scheduler owns timing and durable trigger state, not managed-job execution.
	// The managed attempt must outlive this short-lived scheduler wrapper, so it
	// is parented to the Scheduler engine lifecycle. JobManager then owns the
	// attempt and TaskEngine owns its physical execution/cancellation.
	return e.jobsMgr.TryTrigger(e.ctx, jobID)
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
