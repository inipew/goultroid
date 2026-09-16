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
}

func (h scheduledActionHandler) run(ctx context.Context, definition jobs.JobDefinition) error {
	if len(definition.Payload) > 0 {
		var job scheduler.ScheduledJob
		if err := json.Unmarshal(definition.Payload, &job); err == nil && job.ID > 0 {
			return h.execute(ctx, job)
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
			})
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
	return h.execute(ctx, *job)
}

func (h scheduledActionHandler) execute(ctx context.Context, job scheduler.ScheduledJob) error {
	if job.ActionType == "action" {
		job.ActionType = scheduler.ActionJob
	}
	switch job.ActionType {
	case scheduler.ActionMessage:
		return h.sendMessage(ctx, job)
	case scheduler.ActionCommand:
		return h.executeCommand(ctx, job)
	case scheduler.ActionJob:
		if h.jobs == nil {
			return errors.New("jobs manager is not configured")
		}
		return h.jobs.TryTrigger(ctx, strings.TrimSpace(job.Payload))
	default:
		return fmt.Errorf("unknown scheduled job action type: %s", job.ActionType)
	}
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
	return h.executor.ExecuteExecution(execution, command, h.service())
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
