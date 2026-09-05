package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/scheduler"
)

// Plugin provides commands for scheduling messages, reminders, and periodic commands.
type Plugin struct {
	sched scheduler.Service
}

// New creates a new scheduler Plugin instance.
func New(sched scheduler.Service) *Plugin {
	return &Plugin{
		sched: sched,
	}
}

func (p *Plugin) Name() string {
	return "scheduler"
}

func (p *Plugin) Init() error {
	return nil
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "remind",
			Description: "Set a quick reminder in this chat",
			Usage:       ".remind <duration> <text> or reply to a message with .remind <duration>",
			Category:    "Scheduler",
			Permission:  core.PermissionSudo,
			Handler:     p.handleRemind,
		},
		{
			Name:        "schedule",
			Description: "Schedule a message or command (e.g. .schedule in 30m text or .schedule every 1h .alive)",
			Usage:       ".schedule [in|every] <duration> <payload>",
			Category:    "Scheduler",
			Permission:  core.PermissionSudo,
			Handler:     p.handleSchedule,
		},
		{
			Name:        "schedules",
			Description: "List all active schedules in this chat",
			Usage:       ".schedules",
			Category:    "Scheduler",
			Permission:  core.PermissionSudo,
			Handler:     p.handleList,
		},
		{
			Name:        "cancelschedule",
			Aliases:     []string{"delschedule", "delremind"},
			Description: "Cancel a scheduled job by its ID",
			Usage:       ".cancelschedule <id>",
			Category:    "Scheduler",
			Permission:  core.PermissionSudo,
			Handler:     p.handleCancel,
		},
	}
}

func (p *Plugin) handleRemind(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.Reply("⚠️ Usage: <code>.remind &lt;duration&gt; &lt;text&gt;</code> or reply to a message with <code>.remind &lt;duration&gt;</code>\nExample: <code>.remind 15m Take a break</code>")
		return errors.New("missing arguments")
	}

	durStr := ctx.Args[0]
	dur, err := scheduler.ParseDuration(durStr)
	if err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Invalid duration %q: %v", durStr, err))
		return err
	}

	var text string
	if len(ctx.Args) >= 2 {
		text = strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, ctx.Args[0]))
	} else {
		reply, err := ctx.GetReply()
		if err != nil || reply == nil || reply.Text == "" {
			_ = ctx.Reply("⚠️ Please specify reminder text or reply to a text message.")
			return errors.New("missing reminder text")
		}
		text = reply.Text
	}

	chatID := getChatID(ctx)
	peerType, accessHash := extractPeerInfo(ctx.PeerID)
	when := time.Now().Add(dur)

	job, err := p.sched.ScheduleOnce(ctx.Ctx, chatID, peerType, accessHash, when, scheduler.ActionMessage, text)
	if err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to schedule reminder: %v", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("⏰ <b>Reminder set!</b>\nI will remind you in <code>%s</code>.\n<b>Job ID:</b> <code>#%d</code>", durStr, job.ID))
}

func (p *Plugin) handleSchedule(ctx *core.Context) error {
	if len(ctx.Args) < 2 {
		_ = ctx.Reply("⚠️ Usage: <code>.schedule [in|every] &lt;duration&gt; &lt;text/command&gt;</code>\nExamples:\n• <code>.schedule in 30m .whois @user</code>\n• <code>.schedule every 2h .alive</code>")
		return errors.New("missing arguments")
	}

	isRecurring := false
	durIdx := 0
	payloadIdx := 1

	firstArg := strings.ToLower(ctx.Args[0])
	if firstArg == "every" {
		isRecurring = true
		durIdx = 1
		payloadIdx = 2
	} else if firstArg == "in" {
		durIdx = 1
		payloadIdx = 2
	}

	if len(ctx.Args) <= payloadIdx {
		_ = ctx.Reply("⚠️ Please provide a text or command payload to schedule.")
		return errors.New("missing schedule payload")
	}

	durStr := ctx.Args[durIdx]
	dur, err := scheduler.ParseDuration(durStr)
	if err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Invalid duration %q: %v", durStr, err))
		return err
	}

	// Extract payload
	prefixToStrip := ctx.Args[0] + " " + ctx.Args[1]
	if payloadIdx == 1 {
		prefixToStrip = ctx.Args[0]
	}
	payload := strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, prefixToStrip))
	if payload == "" {
		_ = ctx.Reply("⚠️ Payload cannot be empty.")
		return errors.New("empty payload")
	}

	actionType := scheduler.ActionMessage
	if strings.HasPrefix(payload, ".") {
		actionType = scheduler.ActionCommand
	}

	chatID := getChatID(ctx)
	peerType, accessHash := extractPeerInfo(ctx.PeerID)

	if isRecurring {
		job, err := p.sched.ScheduleRecurring(ctx.Ctx, chatID, peerType, accessHash, dur, actionType, payload)
		if err != nil {
			_ = ctx.Reply(fmt.Sprintf("❌ Failed to create recurring schedule: %v", err))
			return err
		}
		return ctx.Reply(fmt.Sprintf("📅 <b>Recurring schedule created!</b>\n<b>Interval:</b> every <code>%s</code>\n<b>Type:</b> <code>%s</code>\n<b>Action:</b> <code>%s</code>\n<b>Job ID:</b> <code>#%d</code>", durStr, actionType, payload, job.ID))
	}

	when := time.Now().Add(dur)
	job, err := p.sched.ScheduleOnce(ctx.Ctx, chatID, peerType, accessHash, when, actionType, payload)
	if err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to create schedule: %v", err))
		return err
	}
	return ctx.Reply(fmt.Sprintf("📅 <b>Schedule created!</b>\n<b>Due in:</b> <code>%s</code>\n<b>Type:</b> <code>%s</code>\n<b>Action:</b> <code>%s</code>\n<b>Job ID:</b> <code>#%d</code>", durStr, actionType, payload, job.ID))
}

func (p *Plugin) handleList(ctx *core.Context) error {
	chatID := getChatID(ctx)
	jobs, err := p.sched.List(ctx.Ctx, chatID)
	if err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to list schedules: %v", err))
		return err
	}

	if len(jobs) == 0 {
		return ctx.Reply("ℹ️ No active scheduled jobs in this chat.")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "📅 <b>Active Schedules in this chat (%d):</b>\n\n", len(jobs))
	for _, j := range jobs {
		mode := "One-shot"
		if j.IntervalSeconds > 0 {
			mode = fmt.Sprintf("Every %s", time.Duration(j.IntervalSeconds)*time.Second)
		}
		remaining := time.Until(j.NextRunAt).Round(time.Second)
		if remaining < 0 {
			remaining = 0
		}

		payloadSnippet := j.Payload
		if len(payloadSnippet) > 35 {
			payloadSnippet = payloadSnippet[:32] + "..."
		}

		fmt.Fprintf(&sb, "• <b>#%d</b> [%s | %s] <code>%s</code>\n  └ <i>Due in:</i> <code>%s</code>\n",
			j.ID, mode, j.ActionType, payloadSnippet, remaining)
	}

	return ctx.Reply(sb.String())
}

func (p *Plugin) handleCancel(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.Reply("⚠️ Usage: <code>.cancelschedule &lt;id&gt;</code>")
		return errors.New("missing job id")
	}

	idStr := strings.TrimPrefix(ctx.Args[0], "#")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Invalid job ID %q: %v", idStr, err))
		return err
	}

	if err := p.sched.Cancel(ctx.Ctx, id); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to cancel job #%d: %v", id, err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("🗑️ Scheduled job <code>#%d</code> canceled successfully.", id))
}

func getChatID(ctx *core.Context) int64 {
	if ctx.Chat != nil && ctx.Chat.ID != 0 {
		return ctx.Chat.ID
	}
	return ctx.SenderID()
}

func extractPeerInfo(peer tg.InputPeerClass) (string, int64) {
	switch p := peer.(type) {
	case *tg.InputPeerSelf:
		return "self", 0
	case *tg.InputPeerUser:
		return "user", p.AccessHash
	case *tg.InputPeerChat:
		return "chat", 0
	case *tg.InputPeerChannel:
		return "channel", p.AccessHash
	default:
		return "chat", 0
	}
}
