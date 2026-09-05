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
	db      database.Repository
	svcFunc func() core.TelegramServicer
	router  *core.Router
	perms   *core.Permissions
	logger  *zap.Logger

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
	return &Engine{
		db:      db,
		svcFunc: svcFunc,
		router:  router,
		perms:   perms,
		logger:  logger,
		tasks:   make(map[string]context.CancelFunc),
	}
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

// ScheduleOnce saves a one-off task (interval = 0) to be run at a specific future time.
func (e *Engine) ScheduleOnce(
	ctx context.Context,
	chatID int64,
	peerType string,
	accessHash int64,
	when time.Time,
	actionType string,
	payload string,
) (*database.ScheduledJob, error) {
	if peerType == "" {
		peerType = "chat"
	}
	job := &database.ScheduledJob{
		ChatID:          chatID,
		PeerType:        peerType,
		AccessHash:      accessHash,
		ActionType:      actionType,
		Payload:         payload,
		IntervalSeconds: 0,
		NextRunAt:       when,
		CreatedAt:       time.Now(),
	}
	return e.db.CreateScheduledJob(ctx, job)
}

// ScheduleRecurring saves a recurring task (interval > 0) to be run repeatedly.
func (e *Engine) ScheduleRecurring(
	ctx context.Context,
	chatID int64,
	peerType string,
	accessHash int64,
	interval time.Duration,
	actionType string,
	payload string,
) (*database.ScheduledJob, error) {
	if interval <= 0 {
		return nil, errors.New("interval must be greater than zero")
	}
	if peerType == "" {
		peerType = "chat"
	}
	sec := int64(interval.Seconds())
	job := &database.ScheduledJob{
		ChatID:          chatID,
		PeerType:        peerType,
		AccessHash:      accessHash,
		ActionType:      actionType,
		Payload:         payload,
		IntervalSeconds: sec,
		NextRunAt:       time.Now().Add(interval),
		CreatedAt:       time.Now(),
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
	dueJobs, err := e.db.ListDueScheduledJobs(ctx, now)
	if err != nil || len(dueJobs) == 0 {
		return
	}

	for _, job := range dueJobs {
		j := job
		// Immediately update or delete in DB to prevent re-fetching on the next tick
		if j.IntervalSeconds == 0 {
			_ = e.db.DeleteScheduledJob(ctx, j.ID)
		} else {
			nextRun := now.Add(time.Duration(j.IntervalSeconds) * time.Second)
			_ = e.db.UpdateScheduledJobNextRun(ctx, j.ID, nextRun)
		}

		go e.executeJob(ctx, j)
	}
}

func (e *Engine) executeJob(ctx context.Context, job database.ScheduledJob) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Error("scheduled job execution panicked", zap.Int64("job_id", job.ID), zap.Any("panic", r))
		}
	}()

	switch job.ActionType {
	case ActionMessage:
		e.executeSendMessage(ctx, job)
	case ActionCommand:
		e.executeCommand(ctx, job)
	default:
		e.logger.Warn("unknown action type for scheduled job", zap.String("action_type", job.ActionType), zap.Int64("job_id", job.ID))
	}
}

func (e *Engine) executeSendMessage(ctx context.Context, job database.ScheduledJob) {
	if e.svcFunc == nil {
		e.logger.Warn("cannot execute scheduled message: servicer function is nil")
		return
	}
	svc := e.svcFunc()
	if svc == nil {
		e.logger.Warn("cannot execute scheduled message: servicer is nil")
		return
	}

	peer := reconstructInputPeer(job.PeerType, job.ChatID, job.AccessHash)
	text := job.Payload
	// If one-shot and overdue by more than 1 minute, prepend note
	if job.IntervalSeconds == 0 && time.Since(job.NextRunAt) > 1*time.Minute {
		text = "⏰ <b>Reminder</b> (<i>delayed, bot was offline</i>):\n" + job.Payload
	}

	if _, err := svc.SendMessage(ctx, peer, text); err != nil {
		e.logger.Warn("failed to send scheduled message", zap.Int64("chat_id", job.ChatID), zap.Error(err))
	}
}

func (e *Engine) executeCommand(ctx context.Context, job database.ScheduledJob) {
	if e.router == nil {
		e.logger.Warn("cannot execute scheduled command: router is nil")
		return
	}
	if e.svcFunc == nil {
		e.logger.Warn("cannot execute scheduled command: servicer function is nil")
		return
	}
	svc := e.svcFunc()
	if svc == nil {
		e.logger.Warn("cannot execute scheduled command: servicer is nil")
		return
	}

	parsed, isCmd := e.router.Parse(job.Payload)
	if !isCmd {
		e.logger.Warn("scheduled command payload is not a command", zap.String("payload", job.Payload))
		return
	}

	cmd, exists := e.router.Find(parsed.Name)
	if !exists {
		e.logger.Warn("scheduled command not found in router", zap.String("command", parsed.Name))
		return
	}

	peer := reconstructInputPeer(job.PeerType, job.ChatID, job.AccessHash)
	ownerID := int64(0)
	if e.perms != nil {
		ownerID = e.perms.OwnerID
	}

	coreMsg := &core.Message{
		ID:   0,
		Text: job.Payload,
		Date: time.Now(),
	}
	chat := &core.Chat{
		ID:   job.ChatID,
		Type: job.PeerType,
	}
	sender := &core.User{
		ID: ownerID,
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

	if err := cmd.Handler(coreCtx); err != nil {
		e.logger.Warn("scheduled command returned error", zap.String("command", parsed.Name), zap.Error(err))
	}
}

func reconstructInputPeer(peerType string, chatID int64, accessHash int64) tg.InputPeerClass {
	switch peerType {
	case "self":
		return &tg.InputPeerSelf{}
	case "user":
		return &tg.InputPeerUser{UserID: chatID, AccessHash: accessHash}
	case "channel":
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
