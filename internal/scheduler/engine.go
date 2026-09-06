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
	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

// MisfirePolicy controls how late or missed executions of recurring scheduled jobs are handled.
type MisfirePolicy int

const (
	// MisfireRunOnce executes a late recurring job once immediately, resetting next run to now+interval.
	MisfireRunOnce MisfirePolicy = iota
	// MisfireSkip skips missed executions and advances next run to the future without running payload.
	MisfireSkip
	// MisfireCatchUp executes immediately and attempts catch-up.
	MisfireCatchUp
)

// Engine implements Service for managing and dispatching scheduled tasks.
type Engine struct {
	db       database.Repository
	svcFunc  func() core.TelegramServicer
	router   *core.Router
	perms    *core.Permissions
	executor *core.CommandExecutor
	logger   *zap.Logger

	maxConcurrency int
	sem            chan struct{}
	misfirePolicy  MisfirePolicy

	tasks   map[string]context.CancelFunc
	tasksMu sync.RWMutex

	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running bool
	runMu   sync.Mutex
}

var _ Service = (*Engine)(nil)

// NewEngine constructs a new scheduler Engine instance.
func NewEngine(
	db database.Repository,
	svcFunc func() core.TelegramServicer,
	router *core.Router,
	perms *core.Permissions,
	logger *zap.Logger,
) *Engine {
	if logger == nil {
		logger = zap.NewNop()
	}
	executor := core.NewCommandExecutor(logger, nil, 30*time.Second)
	const defaultConcurrency = 4
	return &Engine{
		db:             db,
		svcFunc:        svcFunc,
		router:         router,
		perms:          perms,
		executor:       executor,
		logger:         logger,
		tasks:          make(map[string]context.CancelFunc),
		maxConcurrency: defaultConcurrency,
		sem:            make(chan struct{}, defaultConcurrency),
		misfirePolicy:  MisfireRunOnce,
	}
}

// SetMaxConcurrency configures the maximum concurrent job execution limit.
func (e *Engine) SetMaxConcurrency(n int) {
	if n <= 0 {
		n = 1
	}
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.maxConcurrency = n
	e.sem = make(chan struct{}, n)
}

// SetMisfirePolicy configures the policy for handling late recurring jobs.
func (e *Engine) SetMisfirePolicy(policy MisfirePolicy) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.misfirePolicy = policy
}

// MisfirePolicy returns the configured misfire policy.
func (e *Engine) MisfirePolicy() MisfirePolicy {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	return e.misfirePolicy
}

// SetExecutor overrides the CommandExecutor used for executing scheduled commands.
func (e *Engine) SetExecutor(executor *core.CommandExecutor) {
	if executor != nil {
		e.executor = executor
	}
}

func validateActionType(actionType string) error {
	_, err := ParseActionType(actionType)
	return err
}

// Start boots the background ticker loops for job execution.
// Invariant: lifecycle-bound. All job contexts are children of e.ctx.
// Stop() is authoritative — jobs must not survive scheduler/application shutdown.
// No context.WithoutCancel is used; cancellation must propagate to running jobs.
func (e *Engine) Start(parentCtx context.Context) error {
	e.runMu.Lock()
	defer e.runMu.Unlock()

	if e.running {
		return errors.New("scheduler engine already running")
	}

	e.ctx, e.cancel = context.WithCancel(parentCtx)
	e.running = true

	e.wg.Add(1)
	go e.runLoop(e.ctx)

	e.logger.Info("scheduler engine started")
	return nil
}

// Stop gracefully halts the scheduler engine, waiting for jobs up to a 10s upper bound.
func (e *Engine) Stop() error {
	return e.StopWithTimeout(10 * time.Second)
}

// StopContext gracefully halts the scheduler engine bounded by the provided context.
// It is the production-grade variant: caller controls budget (e.g. 10s slice of 30s global shutdown).
func (e *Engine) StopContext(ctx context.Context) error {
	e.runMu.Lock()
	if !e.running {
		e.runMu.Unlock()
		return nil
	}
	e.running = false
	e.cancel()
	e.runMu.Unlock()

	// Cancel programmatic tasks
	e.tasksMu.Lock()
	for _, cancelTask := range e.tasks {
		cancelTask()
	}
	e.tasks = make(map[string]context.CancelFunc)
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

// StopWithTimeout gracefully halts the scheduler engine and cancels all active periodic tasks
// with an explicit timeout bound on waiting for ongoing jobs.
func (e *Engine) StopWithTimeout(timeout time.Duration) error {
	e.runMu.Lock()
	if !e.running {
		e.runMu.Unlock()
		return nil
	}
	e.running = false
	e.cancel()
	e.runMu.Unlock()

	// Cancel programmatic tasks
	e.tasksMu.Lock()
	for _, cancelTask := range e.tasks {
		cancelTask()
	}
	e.tasks = make(map[string]context.CancelFunc)
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
	case <-time.After(timeout):
		e.logger.Warn("scheduler engine stop timed out waiting for jobs", zap.Duration("timeout", timeout))
		return fmt.Errorf("scheduler engine stop timed out after %v", timeout)
	}
}

// RegisterPeriodicTask registers a non-persisted recurring background task for plugins.
func (e *Engine) RegisterPeriodicTask(name string, interval time.Duration, task TaskFunc) error {
	if name == "" {
		return errors.New("task name cannot be empty")
	}
	if interval <= 0 {
		return errors.New("interval must be positive")
	}
	if task == nil {
		return errors.New("task function cannot be nil")
	}

	e.tasksMu.Lock()
	defer e.tasksMu.Unlock()

	if e.ctx == nil || e.cancel == nil {
		return errors.New("scheduler engine is not running: call Start() first")
	}

	if cancelExisting, exists := e.tasks[name]; exists {
		cancelExisting()
	}

	taskCtx, cancel := context.WithCancel(e.ctx)
	e.tasks[name] = cancel

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-taskCtx.Done():
				return
			case <-ticker.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							e.logger.Error("periodic task panicked", zap.String("task", name), zap.Any("panic", r))
						}
					}()
					if err := task(taskCtx); err != nil {
						e.logger.Warn("periodic task execution error", zap.String("task", name), zap.Error(err))
					}
				}()
			}
		}
	}()

	return nil
}

// UnregisterPeriodicTask cancels and removes a registered programmatic task.
func (e *Engine) UnregisterPeriodicTask(name string) error {
	e.tasksMu.Lock()
	defer e.tasksMu.Unlock()

	if cancelTask, exists := e.tasks[name]; exists {
		cancelTask()
		delete(e.tasks, name)
		return nil
	}
	return errors.New("task not found")
}

// ScheduleOnce saves a one-shot task (interval == 0) to be run at a specific time.
func (e *Engine) ScheduleOnce(
	ctx context.Context,
	chatID int64,
	peerType string,
	accessHash int64,
	when time.Time,
	actionType string,
	payload string,
	creatorID ...int64,
) (*database.ScheduledJob, error) {
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
	job := &database.ScheduledJob{
		ChatID:          chatID,
		PeerType:        peerType,
		AccessHash:      accessHash,
		ActionType:      actionType,
		Payload:         payload,
		IntervalSeconds: 0,
		NextRunAt:       when,
		CreatedAt:       time.Now().UTC(),
		CreatedBy:       createdBy,
		Status:          database.JobStatusPending,
		MaxAttempts:     3,
	}
	return e.db.CreateScheduledJob(ctx, job)
}

// ScheduleRecurring saves a recurring task (interval >= 1s) to be run repeatedly.
func (e *Engine) ScheduleRecurring(
	ctx context.Context,
	chatID int64,
	peerType string,
	accessHash int64,
	interval time.Duration,
	actionType string,
	payload string,
	creatorID ...int64,
) (*database.ScheduledJob, error) {
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
	job := &database.ScheduledJob{
		ChatID:          chatID,
		PeerType:        peerType,
		AccessHash:      accessHash,
		ActionType:      actionType,
		Payload:         payload,
		IntervalSeconds: sec,
		NextRunAt:       time.Now().UTC().Add(interval),
		CreatedAt:       time.Now().UTC(),
		CreatedBy:       createdBy,
		Status:          database.JobStatusPending,
		MaxAttempts:     3,
	}
	return e.db.CreateScheduledJob(ctx, job)
}

// Cancel deletes a scheduled job by its database ID.
func (e *Engine) Cancel(ctx context.Context, jobID int64) error {
	return e.db.DeleteScheduledJob(ctx, jobID)
}

// List returns all active scheduled jobs for a specific chat.
func (e *Engine) List(ctx context.Context, chatID int64) ([]database.ScheduledJob, error) {
	return e.db.ListScheduledJobs(ctx, chatID)
}

// JobHistory returns up to limit recent execution history entries for the given job.
func (e *Engine) JobHistory(ctx context.Context, jobID int64, limit int) ([]database.JobHistoryEntry, error) {
	return e.db.GetJobHistory(ctx, jobID, limit)
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
	// Bounded concurrency: do not claim more jobs than available worker slots
	availableSlots := cap(e.sem) - len(e.sem)
	if availableSlots <= 0 {
		return
	}
	claimBatch := availableSlots
	if claimBatch > 10 {
		claimBatch = 10
	}

	// Claim up to claimBatch due jobs with a 90-second lease (3x default execution timeout)
	claimedJobs, err := e.db.ClaimDueScheduledJobs(ctx, now, claimBatch, 90*time.Second)
	if err != nil {
		e.logger.Error("failed to claim due scheduled jobs", zap.Error(err))
		return
	}
	if len(claimedJobs) == 0 {
		return
	}

	for _, job := range claimedJobs {
		j := job
		// Acquire concurrency semaphore slot
		select {
		case e.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}

		e.wg.Add(1)
		go func(targetJob database.ScheduledJob) {
			defer func() {
				<-e.sem
				e.wg.Done()
			}()
			// Lifecycle-bound child context: cancelled when scheduler stops, inherits timeout/cancellation.
			// No context.WithoutCancel — jobs must not survive shutdown.
			jobCtx, cancel := context.WithCancel(e.ctx)
			defer cancel()
			e.executeJob(jobCtx, targetJob, cancel)
		}(j)
	}
}

func (e *Engine) executeJob(ctx context.Context, job database.ScheduledJob, cancelFn ...context.CancelFunc) {
	var cancel context.CancelFunc
	if len(cancelFn) > 0 {
		cancel = cancelFn[0]
	}

	defer func() {
		if r := recover(); r != nil {
			e.logger.Error("scheduled job execution panicked", zap.Int64("job_id", job.ID), zap.Any("panic", r))
			_ = e.db.FailScheduledJob(ctx, job.ID, job.ClaimToken, fmt.Sprintf("panic: %v", r), 0, 10*time.Second, false, time.Now().UTC())
		}
	}()

	// Start lease renewal heartbeat: extends lease by 90s every 30s for long-running jobs (>90s)
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
					e.logger.Warn("heartbeat lease renewal failed or lease lost, cancelling execution context",
						zap.Int64("job_id", job.ID),
						zap.Error(err),
					)
					if cancel != nil {
						cancel()
					}
					return
				}
				e.logger.Debug("heartbeat renewed job lease", zap.Int64("job_id", job.ID))
			}
		}
	}()

	startTime := time.Now()

	// Check misfire policy for overdue recurring jobs
	if job.IntervalSeconds > 0 && time.Since(job.NextRunAt) > 1*time.Minute {
		if e.misfirePolicy == MisfireSkip {
			e.logger.Warn("recurring scheduled job misfired: skipping execution per MisfireSkip policy",
				zap.Int64("job_id", job.ID),
				zap.Time("next_run_at", job.NextRunAt),
			)
			_ = e.db.CompleteScheduledJob(ctx, job.ID, job.ClaimToken, 0, time.Now().UTC())
			return
		}
	}

	var execErr error
	switch job.ActionType {
	case ActionMessage:
		execErr = e.executeSendMessage(ctx, job)
	case ActionCommand:
		execErr = e.executeCommand(ctx, job)
	default:
		execErr = fmt.Errorf("unknown action type: %s", job.ActionType)
		e.logger.Warn("unknown action type for scheduled job", zap.String("action_type", job.ActionType), zap.Int64("job_id", job.ID))
	}

	now := time.Now().UTC()
	durationMs := now.Sub(startTime).Milliseconds()

	if execErr == nil {
		if err := e.db.CompleteScheduledJob(ctx, job.ID, job.ClaimToken, durationMs, now); err != nil {
			if errors.Is(err, database.ErrJobLeaseLost) {
				e.logger.Warn("scheduled job lease lost or claimed by another worker on complete",
					zap.Int64("job_id", job.ID),
					zap.String("claim_token", job.ClaimToken),
				)
			} else {
				e.logger.Error("failed to complete scheduled job", zap.Int64("job_id", job.ID), zap.Error(err))
			}
		}
	} else {
		isPermanent := core.IsPermanentError(execErr)
		var retryDelay time.Duration

		if !isPermanent {
			// Exponential backoff: 10s * 2^(attempt - 1), capped at 5 minutes
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

		if err := e.db.FailScheduledJob(ctx, job.ID, job.ClaimToken, execErr.Error(), durationMs, retryDelay, isPermanent, now); err != nil {
			if errors.Is(err, database.ErrJobLeaseLost) {
				e.logger.Warn("scheduled job lease lost or claimed by another worker on fail",
					zap.Int64("job_id", job.ID),
					zap.String("claim_token", job.ClaimToken),
				)
			} else {
				e.logger.Error("failed to record scheduled job failure", zap.Int64("job_id", job.ID), zap.Error(err))
			}
		}
	}
}

func (e *Engine) executeSendMessage(ctx context.Context, job database.ScheduledJob) error {
	if e.svcFunc == nil {
		err := errors.New("cannot execute scheduled message: servicer function is nil")
		e.logger.Warn(err.Error())
		return err
	}
	svc := e.svcFunc()
	if svc == nil {
		err := errors.New("cannot execute scheduled message: servicer is nil")
		e.logger.Warn(err.Error())
		return err
	}

	peer := reconstructInputPeer(job.PeerType, job.ChatID, job.AccessHash)
	text := job.Payload
	// If one-shot and overdue by more than 1 minute, prepend note
	if job.IntervalSeconds == 0 && time.Since(job.NextRunAt) > 1*time.Minute {
		text = "⏰ <b>Reminder</b> (<i>delayed, bot was offline</i>):\n" + job.Payload
	}

	if _, err := svc.SendMessage(ctx, peer, text); err != nil {
		e.logger.Warn("failed to send scheduled message", zap.Int64("chat_id", job.ChatID), zap.Error(err))
		return err
	}
	return nil
}

func (e *Engine) executeCommand(ctx context.Context, job database.ScheduledJob) error {
	if e.router == nil {
		err := errors.New("cannot execute scheduled command: router is nil")
		e.logger.Warn(err.Error())
		return err
	}
	if e.svcFunc == nil {
		err := errors.New("cannot execute scheduled command: servicer function is nil")
		e.logger.Warn(err.Error())
		return err
	}
	svc := e.svcFunc()
	if svc == nil {
		err := errors.New("cannot execute scheduled command: servicer is nil")
		e.logger.Warn(err.Error())
		return err
	}

	parsed, isCmd, err := e.router.Parse(job.Payload)
	if err != nil {
		e.logger.Warn("scheduled command payload has syntax error", zap.Error(err), zap.String("payload", job.Payload))
		return fmt.Errorf("%w: %v", core.ErrInvalidArgs, err)
	}
	if !isCmd {
		err := fmt.Errorf("scheduled command payload is not a command: %s", job.Payload)
		e.logger.Warn(err.Error())
		return err
	}

	cmd, exists := e.router.Find(parsed.Name)
	if !exists {
		err := fmt.Errorf("scheduled command not found in router: %s", parsed.Name)
		e.logger.Warn(err.Error())
		return err
	}

	peer := reconstructInputPeer(job.PeerType, job.ChatID, job.AccessHash)

	// SECURITY BOUNDARY & DYNAMIC PRINCIPAL EVALUATION:
	// If CreatedBy == 0, principal is unknown/unprivileged. Do NOT assume OwnerID!
	callerID := job.CreatedBy
	var principal *core.Principal
	if e.perms != nil {
		principal, _ = e.perms.Resolve(ctx, callerID)
	}

	coreMsg := &core.Message{
		ID:         0,
		Text:       job.Payload,
		Date:       time.Now(),
		IsOutgoing: true,
	}
	chat := &core.Chat{
		ID:   job.ChatID,
		Type: job.PeerType,
	}
	sender := &core.User{
		ID: callerID,
	}

	coreCtx := &core.Context{
		Ctx:           ctx,
		CorrelationID: fmt.Sprintf("sched-%d-%d", job.ID, time.Now().UnixMilli()),
		Command:       parsed.Name,
		Args:          parsed.Args,
		RawArgs:       parsed.RawArgs,
		Message:       coreMsg,
		Chat:          chat,
		Sender:        sender,
		Perms:         e.perms,
		Principal:     principal,
		Svc:           svc,
		PeerID:        peer,
	}

	if err := e.executor.Execute(coreCtx, cmd); err != nil {
		e.logger.Warn("scheduled command returned error", zap.String("command", parsed.Name), zap.Error(err))
		return err
	}
	return nil
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

// ParseDuration parses natural duration strings into time.Duration.
// Supports s, m, h, d, as well as English words (minutes, hours, days, etc.).
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errors.New("duration cannot be empty")
	}

	// Normalize common English word variations
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

	// Handle day specifier e.g. "2d", "1d6h"
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
