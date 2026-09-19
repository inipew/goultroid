package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/execution"
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
	actionTimeout                     = 90 * time.Second
	schedulerClaimLease               = 3 * time.Minute
	misfireThreshold                  = time.Minute
	schedulerSettlementSafetyInterval = 30 * time.Second
)

// trackedClaim follows one claimed row until its occurrence settles durably.
type trackedClaim struct {
	jobID        int64
	claimToken   string
	definitionID string
	occurrenceID string
}

type Engine struct {
	db     Repository
	logger *zap.Logger
	access privilegedChecker

	tasks   tasks.Client
	jobsMgr *jobs.Manager

	claimBatchSize int
	misfirePolicy  MisfirePolicy

	wakeChan chan struct{}

	claimMu     sync.Mutex
	claimActive int
	claimZero   chan struct{}
	quiescing   bool

	claimsMu sync.Mutex
	claims   map[int64]*trackedClaim

	periodic *periodicCoordinator

	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	runDone chan struct{}

	running bool
	runMu   sync.Mutex
}

type privilegedChecker interface {
	IsSudo(int64) bool
}

func (e *Engine) SetPrivilegedChecker(checker privilegedChecker) {
	e.runMu.Lock()
	e.access = checker
	e.runMu.Unlock()
}

// PeriodicTaskSnapshot exposes runtime diagnostics without exposing cancel functions or internal scheduler state.
type PeriodicTaskSnapshot struct {
	Owner     string
	Name      string
	Runs      int64
	Failures  int64
	LastRunAt time.Time
	LastError string
}

// PeriodicTaskOptions controls timeout and retry behavior for one periodic task execution.
type PeriodicTaskOptions struct {
	Owner       string
	Timeout     time.Duration
	MaxAttempts int
	RetryDelay  time.Duration
}

var _ Service = (*Engine)(nil)

func NewEngine(db Repository, logger *zap.Logger) *Engine {
	if logger == nil {
		logger = zap.NewNop()
	}
	const defaultBatchSize = 4
	engine := &Engine{
		db: db, logger: logger, claimBatchSize: defaultBatchSize,
		misfirePolicy: MisfireRunOnce, wakeChan: make(chan struct{}, 1),
	}
	engine.periodic = newPeriodicCoordinator(logger)
	engine.claims = make(map[int64]*trackedClaim)
	engine.claimZero = make(chan struct{})
	close(engine.claimZero)
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

// SetMaxConcurrency is deprecated. Physical scheduler concurrency is governed by TaskEngine.
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

func (e *Engine) SetTasks(client tasks.Client) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.tasks = client
}

func (e *Engine) SetJobsManager(jobsMgr *jobs.Manager) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	if e.jobsMgr != nil && e.jobsMgr != jobsMgr {
		e.jobsMgr.SetScheduleWake(nil)
	}
	e.jobsMgr = jobsMgr
	if jobsMgr == nil {
		return
	}
	jobsMgr.SetScheduleWake(func() {
		e.notifyWake()
		if e.periodic != nil {
			e.periodic.notify()
		}
	})
	if err := jobsMgr.RegisterHandler(periodicHandlerType, e.runPeriodicAction); err != nil {
		e.logger.Warn("register periodic action handler", zap.Error(err))
	}
	if e.periodic != nil {
		e.periodic.SetJobsManager(jobsMgr)
	}
}

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

func (e *Engine) IsRunning() bool {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	return e.running
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
	if e.db == nil {
		return errors.New("scheduler repository is required")
	}
	if e.tasks == nil {
		return errors.New("scheduler task client is required")
	}
	if e.jobsMgr == nil {
		return errors.New("scheduler jobs manager is required")
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
	e.claimActive = 0
	e.claimZero = make(chan struct{})
	close(e.claimZero)
	e.claimMu.Unlock()
	e.running = true
	e.runDone = make(chan struct{})
	go e.runLoop(e.ctx, e.runDone)
	e.logger.Info("scheduler engine started")
	return nil
}

var (
	_ runtime.Component     = (*Engine)(nil)
	_ runtime.ForcedStopper = (*Engine)(nil)
)

func (e *Engine) Name() string                   { return "scheduler" }
func (e *Engine) Dependencies() []string         { return []string{"jobs", "taskengine"} }
func (e *Engine) Stop(ctx context.Context) error { return e.StopContext(ctx) }

func (e *Engine) ForceStop(context.Context) error {
	e.claimMu.Lock()
	e.quiescing = true
	e.claimMu.Unlock()
	e.runMu.Lock()
	if e.cancel != nil {
		e.cancel()
	}
	e.running = false
	e.runMu.Unlock()
	return nil
}

func (e *Engine) beginClaimBatch() bool {
	e.claimMu.Lock()
	defer e.claimMu.Unlock()
	if e.quiescing {
		return false
	}
	if e.claimActive == 0 {
		e.claimZero = make(chan struct{})
	}
	e.claimActive++
	return true
}

func (e *Engine) endClaimBatch() {
	e.claimMu.Lock()
	defer e.claimMu.Unlock()
	if e.claimActive <= 0 {
		return
	}
	e.claimActive--
	if e.claimActive == 0 {
		close(e.claimZero)
	}
}

func (e *Engine) Quiesce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.claimMu.Lock()
	e.quiescing = true
	done := e.claimZero
	e.claimMu.Unlock()
	e.notifyWake()

	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) Health(ctx context.Context) runtime.ComponentHealth {
	e.runMu.Lock()
	running := e.running
	e.runMu.Unlock()
	if !running {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "scheduler engine is not running"}
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

	e.runMu.Lock()
	done := e.runDone
	e.runMu.Unlock()
	if done == nil {
		return nil
	}
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

func (e *Engine) RegisterPeriodicTask(name string, interval time.Duration, task TaskFunc) error {
	return e.RegisterPeriodicTaskWithOptions(name, interval, PeriodicTaskOptions{Owner: "runtime"}, task)
}

func (e *Engine) RegisterPeriodicTaskOwned(owner, name string, interval time.Duration, task TaskFunc) error {
	return e.RegisterPeriodicTaskWithOptions(name, interval, PeriodicTaskOptions{Owner: owner}, task)
}

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

func (e *Engine) UnregisterPeriodicTaskOwned(owner, name string) error {
	if e.periodic == nil {
		return errors.New("periodic coordinator is not configured")
	}
	return e.periodic.UnregisterOwned(owner, name)
}

func (e *Engine) UnregisterPeriodicTasksByOwner(owner string) int {
	if e.periodic == nil {
		return 0
	}
	return e.periodic.UnregisterByOwner(owner)
}

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
	if err := e.registerScheduledDefinition(ctx, res); err != nil {
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
		return nil, fmt.Errorf("recurring interval must be at least 1 second (got %v)", interval)
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
	if err := e.registerScheduledDefinition(ctx, res); err != nil {
		_ = e.db.DeleteScheduledJob(ctx, res.ID)
		return nil, err
	}
	e.notifyWake()
	return res, nil
}

func scheduledDefinitionID(jobID int64) string { return fmt.Sprintf("scheduler:job:%d", jobID) }
func redesignedScheduleID(jobID int64) string  { return fmt.Sprintf("sched:scheduled:%d", jobID) }

func (e *Engine) registerScheduledDefinition(ctx context.Context, job *ScheduledJob) error {
	if job == nil || e.jobsMgr == nil {
		return errors.New("scheduler jobs manager is not configured")
	}
	if job.ActionType == ActionJob {
		targetID := strings.TrimSpace(job.Payload)
		if targetID == "" {
			return errors.New("empty job id in scheduled managed job payload")
		}
		if _, ok := e.jobsMgr.Definition(targetID); !ok {
			return fmt.Errorf("managed job definition not found: %s", targetID)
		}
		// No scheduler.action wrapper definition: the timing row will submit a
		// target occurrence directly when its due slot is claimed.
		return e.saveRedesignedSchedule(ctx, job, targetID)
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode scheduled action: %w", err)
	}
	definitionID := scheduledDefinitionID(job.ID)
	if err := e.jobsMgr.Register(jobs.JobDefinition{
		ID:          definitionID,
		ScopeOwner:  scheduledTaskScope(job.ID),
		QuotaOwner:  "scheduler",
		HandlerType: "scheduler.action",
		Payload:     payload,
		Pool:        "scheduler",
		Class:       string(tasks.PriorityMaintenance),
		Timeout:     actionTimeout,
		RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 1},
		Enabled:     true,
	}); err != nil {
		return err
	}
	return e.saveRedesignedSchedule(ctx, job, definitionID)
}

func (e *Engine) saveRedesignedSchedule(ctx context.Context, job *ScheduledJob, definitionID string) error {
	recurrence := "once"
	interval := time.Duration(0)
	if job.IntervalSeconds > 0 {
		recurrence = "interval"
		interval = time.Duration(job.IntervalSeconds) * time.Second
	}
	return e.jobsMgr.SaveSchedule(ctx, jobs.JobSchedule{
		ID: redesignedScheduleID(job.ID), JobID: definitionID,
		Recurrence: recurrence, Interval: interval, Timezone: "UTC", NextDueAt: job.NextRunAt,
		MisfirePolicy: jobs.MisfireRunOnce, OverlapPolicy: jobs.OverlapForbid,
		Enabled: true, Revision: 1,
	})
}

func (e *Engine) Cancel(ctx context.Context, jobID int64) error {
	if jobID <= 0 {
		return errors.New("invalid scheduled job ID")
	}
	if err := e.db.DeleteScheduledJob(ctx, jobID); err != nil {
		return err
	}
	if e.jobsMgr != nil {
		if err := e.jobsMgr.DisableSchedule(ctx, redesignedScheduleID(jobID)); err != nil {
			return fmt.Errorf("disable redesigned schedule: %w", err)
		}
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
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
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

func scheduledTaskScope(jobID int64) string { return fmt.Sprintf("scheduler:job:%d", jobID) }

func (e *Engine) runLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		var nextDelay time.Duration
		var hasDeadline bool
		e.reconcileSettledClaims(ctx)
		if ctx.Err() != nil {
			return
		}

		redesigned, modeErr := e.jobsMgr.ScheduleCutoverActive(ctx)
		if modeErr != nil {
			if errors.Is(modeErr, context.Canceled) || ctx.Err() != nil {
				return
			}
			e.logger.Error("read scheduler cutover mode", zap.Error(modeErr))
		}
		var earliest time.Time
		var found bool
		var err error
		if redesigned {
			earliest, found, err = e.jobsMgr.EarliestScheduleDue(ctx)
		} else {
			earliest, found, err = e.db.GetEarliestDueTime(ctx)
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return
			}
			e.logger.Error("failed to query earliest due scheduled job", zap.Error(err))
			nextDelay = time.Second
			hasDeadline = true
		} else if found {
			hasDeadline = true
			if earliest.Before(now) || earliest.Equal(now) {
				nextDelay = 0
			} else {
				nextDelay = earliest.Sub(now)
			}
		}

		if hasDeadline && nextDelay <= 0 {
			claimed := 0
			if redesigned {
				claimed = e.processRedesignedSchedules(ctx, now)
			} else {
				claimed = e.processDueJobs(ctx, now)
			}
			if claimed == 0 {
				nextDelay = 250 * time.Millisecond
				hasDeadline = true
			} else {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
		}

		// Active legacy claims need settlement reconciliation. This polling is
		// work-driven; with no claims and no schedule the loop sleeps on wake only.
		if e.trackedClaimCount() > 0 && (!hasDeadline || nextDelay > schedulerSettlementSafetyInterval) {
			nextDelay = schedulerSettlementSafetyInterval
			hasDeadline = true
		}

		if !hasDeadline {
			select {
			case <-ctx.Done():
				return
			case <-e.wakeChan:
				continue
			}
		}

		if timer == nil {
			timer = time.NewTimer(nextDelay)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(nextDelay)
		}
		select {
		case <-ctx.Done():
			return
		case <-e.wakeChan:
		case <-timer.C:
		}
	}
}
func (e *Engine) processRedesignedSchedules(ctx context.Context, now time.Time) int {
	if !e.beginClaimBatch() {
		return 0
	}
	defer e.endClaimBatch()
	limit := e.claimBatchSize
	if limit <= 0 {
		limit = 1
	}
	processed, err := e.jobsMgr.ProcessDueSchedules(ctx, now, limit)
	if err != nil {
		e.logger.Error("process redesigned schedules", zap.Error(err))
		return 0
	}
	return processed
}

func (e *Engine) processDueJobs(ctx context.Context, now time.Time) int {
	if !e.beginClaimBatch() {
		return 0
	}
	defer e.endClaimBatch()

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
		// Misfire is a WHEN policy. Resolve it before materializing any
		// JobOccurrence/JobAttempt so a skipped slot consumes no execution budget.
		if e.shouldSkipMisfire(j) {
			e.logger.Warn("recurring scheduled job misfired: skipping execution", zap.Int64("job_id", j.ID), zap.Time("next_run_at", j.NextRunAt))
			stateCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			cerr := e.db.CompleteScheduledJob(stateCtx, j.ID, j.ClaimToken, 0, time.Now().UTC())
			cancel()
			if cerr != nil && !errors.Is(cerr, ErrJobLeaseLost) {
				e.logger.Warn("failed to advance skipped recurring job", zap.Int64("job_id", j.ID), zap.Error(cerr))
			}
			e.notifyWake()
			continue
		}

		occurrenceKey := fmt.Sprintf("sched:%d:%d", j.ID, j.NextRunAt.UTC().UnixNano())
		definitionID := scheduledDefinitionID(j.ID)
		if j.ActionType == ActionJob {
			definitionID = strings.TrimSpace(j.Payload)
			if definitionID == "" {
				e.onSubmitRejected(ctx, j, occurrenceKey, execution.WithSemantics(
					errors.New("empty job id in scheduled managed job payload"),
					execution.Semantics{Disposition: execution.DispositionPermanent, Code: "empty_managed_job_id"},
				))
				continue
			}
			if _, ok := e.jobsMgr.Definition(definitionID); !ok {
				e.onSubmitRejected(ctx, j, occurrenceKey, execution.WithSemantics(
					fmt.Errorf("managed job definition not found: %s", definitionID),
					execution.Semantics{Disposition: execution.DispositionPermanent, Code: "managed_job_not_found"},
				))
				continue
			}
		}

		_, occurrenceID, submitErr := e.jobsMgr.SubmitOccurrence(e.ctx, definitionID, occurrenceKey)
		if submitErr != nil {
			e.onSubmitRejected(ctx, j, occurrenceKey, submitErr)
			continue
		}
		e.trackClaim(j.ID, j.ClaimToken, definitionID, occurrenceID)
	}
	return len(claimedJobs)
}

func schedulerSubmissionFailurePolicy(err error) (retryDelay time.Duration, permanent bool) {
	semantics := execution.SemanticsOf(err)
	retryDelay = semantics.RetryAfter
	if retryDelay <= 0 {
		retryDelay = time.Second
	}
	permanent = semantics.Disposition == execution.DispositionPermanent ||
		semantics.Disposition == execution.DispositionRejected
	return retryDelay, permanent
}

func (e *Engine) onSubmitRejected(ctx context.Context, job ScheduledJob, occurrenceKey string, submitErr error) {
	definitionID := scheduledDefinitionID(job.ID)
	if job.ActionType == ActionJob {
		definitionID = strings.TrimSpace(job.Payload)
	}
	stateCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if occ, oerr := e.jobsMgr.OccurrenceByKey(stateCtx, occurrenceKey); oerr == nil && occ != nil {
		switch occ.State {
		case jobs.OccurrenceReady, jobs.OccurrenceDispatched:
			if rerr := e.db.RenewJobLease(stateCtx, job.ID, job.ClaimToken, schedulerClaimLease, time.Now().UTC()); rerr != nil && !errors.Is(rerr, ErrJobLeaseLost) {
				e.logger.Error("failed to renew lease for live scheduled occurrence", zap.Int64("job_id", job.ID), zap.Error(rerr))
			} else {
				e.trackClaim(job.ID, job.ClaimToken, definitionID, occ.ID)
			}
			return
		case jobs.OccurrenceCompleted, jobs.OccurrenceFailed, jobs.OccurrenceCancelled:
			// Admission may fail after canonical materialization and then settle
			// quickly (e.g. aborted-before-start + automatic recovery). Track the
			// canonical terminal occurrence so row reconciliation records the real
			// outcome instead of releasing the slot and creating another logical run.
			e.trackClaim(job.ID, job.ClaimToken, definitionID, occ.ID)
			return
		}
	}
	e.logger.Error("failed to admit scheduled occurrence", zap.Int64("job_id", job.ID), zap.Error(submitErr))
	retryDelay, permanent := schedulerSubmissionFailurePolicy(submitErr)
	if failErr := e.db.FailScheduledJob(stateCtx, job.ID, job.ClaimToken, submitErr.Error(), 0, retryDelay, permanent, time.Now().UTC()); failErr != nil && !errors.Is(failErr, ErrJobLeaseLost) {
		e.logger.Error("failed to release rejected scheduled claim", zap.Int64("job_id", job.ID), zap.Error(failErr))
	}
}

func (e *Engine) trackClaim(jobID int64, claimToken, definitionID, occurrenceID string) {
	e.claimsMu.Lock()
	defer e.claimsMu.Unlock()
	if e.claims == nil {
		e.claims = make(map[int64]*trackedClaim)
	}
	e.claims[jobID] = &trackedClaim{jobID: jobID, claimToken: claimToken, definitionID: definitionID, occurrenceID: occurrenceID}
}

func (e *Engine) untrackClaim(jobID int64) {
	e.claimsMu.Lock()
	defer e.claimsMu.Unlock()
	delete(e.claims, jobID)
}

func (e *Engine) trackedClaimCount() int {
	e.claimsMu.Lock()
	defer e.claimsMu.Unlock()
	return len(e.claims)
}

func (e *Engine) reconcileSettledClaims(ctx context.Context) {
	e.claimsMu.Lock()
	pending := make([]*trackedClaim, 0, len(e.claims))
	for _, c := range e.claims {
		pending = append(pending, c)
	}
	e.claimsMu.Unlock()
	for _, c := range pending {
		if ctx.Err() != nil {
			return
		}
		e.reconcileClaim(ctx, c)
	}
}

func (e *Engine) reconcileClaim(ctx context.Context, c *trackedClaim) {
	stateCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	row, err := e.db.GetScheduledJob(stateCtx, c.jobID)
	if err != nil || row == nil {
		e.untrackClaim(c.jobID)
		return
	}
	if row.ClaimToken != c.claimToken {
		e.untrackClaim(c.jobID)
		return
	}
	occ, err := e.jobsMgr.GetOccurrence(stateCtx, c.occurrenceID)
	if err != nil || occ == nil {
		return
	}
	now := time.Now().UTC()
	switch occ.State {
	case jobs.OccurrenceCompleted:
		if cerr := e.db.CompleteScheduledJob(stateCtx, c.jobID, c.claimToken, occurrenceDurationMs(stateCtx, e.jobsMgr, c.occurrenceID), now); cerr != nil && !errors.Is(cerr, ErrJobLeaseLost) {
			e.logger.Error("failed to complete scheduled job", zap.Int64("job_id", c.jobID), zap.Error(cerr))
			return
		}
		e.pruneSettledOccurrences(ctx, c.definitionID)
		e.untrackClaim(c.jobID)
		e.notifyWake()
	case jobs.OccurrenceFailed:
		if ferr := e.db.FailScheduledJob(stateCtx, c.jobID, c.claimToken, occurrenceError(stateCtx, e.jobsMgr, c.occurrenceID), 0, 0, true, now); ferr != nil && !errors.Is(ferr, ErrJobLeaseLost) {
			e.logger.Error("failed to record scheduled job failure", zap.Int64("job_id", c.jobID), zap.Error(ferr))
			return
		}
		e.pruneSettledOccurrences(ctx, c.definitionID)
		e.untrackClaim(c.jobID)
		e.notifyWake()
	case jobs.OccurrenceCancelled:
		if cerr := e.db.CompleteScheduledJob(stateCtx, c.jobID, c.claimToken, 0, now); cerr != nil && !errors.Is(cerr, ErrJobLeaseLost) {
			e.logger.Error("failed to advance cancelled scheduled job", zap.Int64("job_id", c.jobID), zap.Error(cerr))
			return
		}
		e.pruneSettledOccurrences(ctx, c.definitionID)
		e.untrackClaim(c.jobID)
		e.notifyWake()
	}
}

func (e *Engine) pruneSettledOccurrences(ctx context.Context, definitionID string) {
	if e.jobsMgr == nil || definitionID == "" {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, _ = e.jobsMgr.PruneOccurrences(pctx, definitionID, time.Now().UTC().Add(-24*time.Hour), 500)
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

func (e *Engine) shouldSkipMisfire(job ScheduledJob) bool {
	if job.IntervalSeconds <= 0 {
		return false
	}
	if time.Since(job.NextRunAt) <= misfireThreshold {
		return false
	}
	return e.MisfirePolicy() == MisfireSkip
}

func (e *Engine) ScheduleManagedJob(ctx context.Context, jobID string, when time.Time, interval time.Duration) (*ScheduledJob, error) {
	if interval <= 0 {
		return e.ScheduleOnce(ctx, 0, "internal", 0, when, ActionJob, jobID)
	}
	return e.ScheduleRecurring(ctx, 0, "internal", 0, interval, ActionJob, jobID)
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
