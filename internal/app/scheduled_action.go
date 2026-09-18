package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/scheduler"
	"github.com/inipew/goultroid/internal/tasks"
)

// scheduledActionHandler is the application adapter for scheduled message and
// command rows. The timing-only scheduler never imports Telegram infrastructure.
type scheduledActionHandler struct {
	repo     scheduler.Repository
	service  func() core.TelegramServicer
	router   *core.Router
	perms    *core.Permissions
	executor *core.CommandExecutor
	jobs     *jobs.Manager
	tasks    tasks.Client
}

func (h scheduledActionHandler) run(ctx context.Context, definition jobs.JobDefinition) error {
	if len(definition.Payload) > 0 {
		var job scheduler.ScheduledJob
		if err := json.Unmarshal(definition.Payload, &job); err == nil && job.ID > 0 {
			return h.execute(ctx, job, true)
		}
		var migrated struct {
			LegacyID   int64  `json:"legacy_id"`
			ChatID     int64  `json:"chat_id"`
			PeerType   string `json:"peer_type"`
			AccessHash int64  `json:"access_hash"`
			ActionType string `json:"action_type"`
			Payload    string `json:"payload_data"`
		}
		if err := json.Unmarshal(definition.Payload, &migrated); err == nil && migrated.LegacyID > 0 {
			return h.execute(ctx, scheduler.ScheduledJob{
				ID: migrated.LegacyID, ChatID: migrated.ChatID, PeerType: migrated.PeerType,
				AccessHash: migrated.AccessHash, ActionType: migrated.ActionType, Payload: migrated.Payload,
			}, true)
		}
	}
	jobID, err := strconv.ParseInt(strings.TrimPrefix(definition.ID, "scheduler:job:"), 10, 64)
	if err != nil || jobID <= 0 {
		return fmt.Errorf("invalid scheduler job definition: %s", definition.ID)
	}
	job, err := h.repo.GetScheduledJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job == nil || job.ClaimToken == "" {
		return errors.New("scheduled job is no longer claimed")
	}
	return h.execute(ctx, *job, false)
}

func (h scheduledActionHandler) execute(ctx context.Context, job scheduler.ScheduledJob, reconcileProjection bool) error {
	if job.ActionType == "action" {
		job.ActionType = scheduler.ActionJob
	}
	var err error
	switch job.ActionType {
	case scheduler.ActionMessage:
		err = h.sendMessage(ctx, job)
	case scheduler.ActionCommand:
		err = h.executeCommand(ctx, job)
	case scheduler.ActionJob:
		if h.jobs == nil {
			return errors.New("jobs manager is not configured")
		}
		err = h.jobs.TryTrigger(ctx, strings.TrimSpace(job.Payload))
	default:
		return fmt.Errorf("unknown scheduled job action type: %s", job.ActionType)
	}
	if err != nil {
		return err
	}
	// During redesigned cutover scheduled_jobs is a compatibility projection,
	// not an execution owner. Reconcile it after the side effect so list/access
	// APIs do not expose an already-completed one-shot forever.
	if reconcileProjection && h.repo != nil && job.ID > 0 {
		if job.IntervalSeconds <= 0 {
			_ = h.repo.DeleteScheduledJob(ctx, job.ID)
		} else {
			next := job.NextRunAt
			step := time.Duration(job.IntervalSeconds) * time.Second
			for !next.After(time.Now().UTC()) {
				next = next.Add(step)
			}
			_ = h.repo.UpdateScheduledJobNextRun(ctx, job.ID, next)
		}
	}
	return nil
}

func (h scheduledActionHandler) sendMessage(ctx context.Context, job scheduler.ScheduledJob) error {
	if h.service == nil || h.service() == nil {
		return errors.New("telegram service is not configured")
	}
	text := job.Payload
	if job.IntervalSeconds == 0 && time.Since(job.NextRunAt) > time.Minute {
		text = "⏰ <b>Reminder</b> (<i>delayed, bot was offline</i>):\n" + text
	}
	_, err := h.service().SendMessage(ctx, scheduledPeer(job), text)
	return err
}

func scheduledCommandPool(resources []tasks.ResourceRequirement) tasks.PoolID {
	hasDownload := false
	for _, requirement := range resources {
		switch requirement.Name {
		case "media":
			return "media-process"
		case "download":
			hasDownload = true
		}
	}
	if hasDownload {
		return "download"
	}
	return "general"
}

func scheduledCommandResultError(res tasks.TaskResult) error {
	if res.IsSuccess() {
		return nil
	}
	if res.Failure.Message != "" {
		return errors.New(res.Failure.Message)
	}
	return fmt.Errorf("scheduled command task %s finished with outcome %s (%s)", res.TaskID, res.Outcome, res.Cause)
}

func (h scheduledActionHandler) executeCommand(ctx context.Context, job scheduler.ScheduledJob) error {
	if h.router == nil || h.executor == nil || h.service == nil || h.service() == nil {
		return errors.New("scheduled command dependencies are not configured")
	}
	parsed, isCommand, err := h.router.Parse(job.Payload)
	if err != nil {
		return fmt.Errorf("%w: %v", core.ErrInvalidArgs, err)
	}
	if !isCommand {
		return fmt.Errorf("scheduled command payload is not a command: %s", job.Payload)
	}
	command, ok := h.router.Find(parsed.Name)
	if !ok {
		return fmt.Errorf("scheduled command not found in router: %s", parsed.Name)
	}
	var principal *core.Principal
	if h.perms != nil {
		principal, _ = h.perms.Resolve(ctx, job.CreatedBy)
	}
	execution := core.CommandExecution{
		Ctx: ctx, Source: core.ExecutionScheduled, Command: parsed.Name,
		Args: parsed.Args, RawArgs: parsed.RawArgs, Principal: principal,
		Perms: h.perms, Chat: &core.Chat{ID: job.ChatID, Type: job.PeerType},
		Sender: &core.User{ID: job.CreatedBy}, PeerID: scheduledPeer(job),
		CorrelationID: fmt.Sprintf("sched-%d-%d", job.ID, time.Now().UnixMilli()),
	}

	if len(command.Resources) == 0 {
		return h.executor.ExecuteExecution(execution, command, h.service())
	}
	if h.tasks == nil {
		return errors.New("scheduled command task client is not configured")
	}

	owner := "scheduler"
	if job.CreatedBy != 0 {
		owner = fmt.Sprintf("scheduler:user:%d", job.CreatedBy)
	}
	workSpec := tasks.WorkSpec{
		ID:               tasks.TaskID(fmt.Sprintf("scheduled-command:%d:%d", job.ID, time.Now().UnixNano())),
		QuotaOwner:       tasks.OwnerID(owner),
		Pool:             scheduledCommandPool(command.Resources),
		Class:            tasks.PriorityMaintenance,
		OrderingKey:      fmt.Sprintf("scheduled-command:%d", job.ID),
		ExecutionTimeout: command.Timeout,
		Resources:        append([]tasks.ResourceRequirement(nil), command.Resources...),
	}
	if deadline, ok := ctx.Deadline(); ok {
		workSpec.QueueDeadline = deadline
	}
	workSpec.Handler = func(taskCtx context.Context) error {
		runCtx, cancel := context.WithCancel(taskCtx)
		defer cancel()
		stopWatching := context.AfterFunc(ctx, cancel)
		defer stopWatching()
		return h.executor.ExecuteExecution(execution.WithContext(runCtx), command, h.service())
	}

	ticket, err := h.tasks.Submit(ctx, workSpec)
	if err != nil {
		return err
	}
	res, waitErr := ticket.Wait(ctx)
	if waitErr != nil {
		_, _ = h.tasks.Cancel(ticket.TaskID(), tasks.CauseTimeout)
		return waitErr
	}
	return scheduledCommandResultError(res)
}

func scheduledPeer(job scheduler.ScheduledJob) tg.InputPeerClass {
	switch job.PeerType {
	case "self":
		return &tg.InputPeerSelf{}
	case "user":
		return &tg.InputPeerUser{UserID: job.ChatID, AccessHash: job.AccessHash}
	case "channel", "supergroup":
		return &tg.InputPeerChannel{ChannelID: job.ChatID, AccessHash: job.AccessHash}
	default:
		return &tg.InputPeerChat{ChatID: job.ChatID}
	}
}
