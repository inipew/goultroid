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

// Engine implements Service for managing and dispatching scheduled tasks.
type Engine struct {
	db       database.Repository
	svcFunc  func() core.TelegramServicer
	router   *core.Router
	perms    *core.Permissions
	executor *core.CommandExecutor
	logger   *zap.Logger

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
	return &Engine{
		db:       db,
		svcFunc:  svcFunc,
		router:   router,
		perms:    perms,
		executor: executor,
		logger:   logger,
		tasks:    make(map[string]context.CancelFunc),
	}
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

// Stop gracefully halts the scheduler engine and cancels all active periodic tasks.
func (e *Engine) Stop() error {
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

	e.wg.Wait()
	e.logger.Info("scheduler engine stopped")
	return nil
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
	// Claim up to 10 due jobs with a 30-second lease
	claimedJobs, err := e.db.ClaimDueScheduledJobs(ctx, now, 10, 30*time.Second)
	if err != nil {
		e.logger.Error("failed to claim due scheduled jobs", zap.Error(err))
		return
	}
	if len(claimedJobs) == 0 {
		return
	}

	for _, job := range claimedJobs {
		j := job
		e.wg.Add(1)
		go func(targetJob database.ScheduledJob) {
			defer e.wg.Done()
			e.executeJob(ctx, targetJob)
		}(j)
	}
}

func (e *Engine) executeJob(ctx context.Context, job database.ScheduledJob) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Error("scheduled job execution panicked", zap.Int64("job_id", job.ID), zap.Any("panic", r))
			_ = e.db.FailScheduledJob(ctx, job.ID, fmt.Sprintf("panic: %v", r), 10*time.Second, time.Now().UTC())
		}
	}()

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
	if execErr == nil {
		if err := e.db.CompleteScheduledJob(ctx, job.ID, now); err != nil {
			e.logger.Error("failed to complete scheduled job", zap.Int64("job_id", job.ID), zap.Error(err))
		}
	} else {
		// Exponential backoff: 10s * 2^(attempt - 1), capped at 5 minutes
		attempt := job.AttemptCount
		if attempt < 1 {
			attempt = 1
		}
		backoffMultiplier := 1 << (attempt - 1)
		if backoffMultiplier > 30 {
			backoffMultiplier = 30
		}
		retryDelay := time.Duration(10*backoffMultiplier) * time.Second

		if err := e.db.FailScheduledJob(ctx, job.ID, execErr.Error(), retryDelay, now); err != nil {
			e.logger.Error("failed to record scheduled job failure", zap.Int64("job_id", job.ID), zap.Error(err))
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

	parsed, isCmd := e.router.Parse(job.Payload)
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

	// SECURITY BOUNDARY:
	// If CreatedBy == 0, principal is unknown/unprivileged. Do NOT assume OwnerID!
	callerID := job.CreatedBy

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
		Ctx:     ctx,
		Command: parsed.Name,
		Args:    parsed.Args,
		RawArgs: parsed.RawArgs,
		Message: coreMsg,
		Chat:    chat,
		Sender:  sender,
		Perms:   e.perms,
		Svc:     svc,
		PeerID:  peer,
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
