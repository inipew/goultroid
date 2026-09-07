package scheduler

import (
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/scheduler"
)

var (
	_ execution.CapabilityProvider = (*Plugin)(nil)
)

// Plugin provides commands for scheduling messages, reminders, and periodic commands.
type Plugin struct {
	sched scheduler.Service
}

// New creates a new scheduler Plugin instance.
func New(sched scheduler.Service) *Plugin {
	return &Plugin{sched: sched}
}

func (p *Plugin) Name() string { return "scheduler" }

func (p *Plugin) Init() error { return nil }

// Capabilities declares the capabilities provided by this plugin (§4, §28 bug16_1).
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{
		{
			ID:          "scheduler",
			Name:        "Scheduler",
			Description: "Message, reminder, and recurring command scheduler",
			Category:    "Scheduler",
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		},
	}
}

func (p *Plugin) Commands() []core.Command {
	schedSurfaces := execution.SurfaceUserbot | execution.SurfaceAssistant
	return []core.Command{
		{
			Name: "remind", Description: "Set a quick reminder in this chat",
			Usage: ".remind <duration> <text> or reply to a message with .remind <duration>",
			Category: "Scheduler", Permission: core.PermissionSudo, Surfaces: schedSurfaces, Handler: p.handleRemind,
		},
		{
			Name: "schedule", Description: "Schedule a message or command (e.g. .schedule in 30m text or .schedule every 1h .alive)",
			Usage: ".schedule [in|every] <duration> <text/command>", Category: "Scheduler", Permission: core.PermissionSudo, Surfaces: schedSurfaces, Handler: p.handleSchedule,
		},
		{
			Name: "schedules", Description: "List all active schedules in this chat", Usage: ".schedules",
			Category: "Scheduler", Permission: core.PermissionSudo, Surfaces: schedSurfaces, Handler: p.handleList,
		},
		{
			Name: "cancelschedule", Aliases: []string{"unschedule", "delschedule", "delremind"},
			Description: "Cancel a scheduled job by its ID", Usage: ".cancelschedule <id>", Category: "Scheduler", Permission: core.PermissionSudo, Surfaces: schedSurfaces, Handler: p.handleCancel,
		},
		{
			Name: "schedhistory", Aliases: []string{"jobhistory", "schedlog"},
			Description: "Show the last execution history entries for a scheduled job", Usage: ".schedhistory <id> [limit]", Category: "Scheduler", Permission: core.PermissionSudo, Surfaces: schedSurfaces, Handler: p.handleSchedHistory,
		},
	}
}

func (p *Plugin) handleRemind(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.EditOrReply("⚠️ Usage: <code>.remind &lt;duration&gt; &lt;text&gt;</code> or reply to a message with <code>.remind &lt;duration&gt;</code>\nExample: <code>.remind 15m Take a break</code>")
		return errors.New("missing arguments")
	}

	durStr := ctx.Args[0]
	dur, err := scheduler.ParseDuration(durStr)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Invalid duration %q: %v", durStr, err))
		return err
	}

	var text string
	if len(ctx.Args) >= 2 {
		text = strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, ctx.Args[0]))
	} else {
		reply, err := ctx.GetReply()
		if err != nil || reply == nil || reply.Text == "" {
			_ = ctx.EditOrReply("⚠️ Please specify reminder text or reply to a text message.")
			return errors.New("missing reminder text")
		}
		text = reply.Text
	}

	chatID := getChatID(ctx)
	peerType, accessHash := extractPeerInfo(ctx.PeerID)
	when := time.Now().Add(dur)

	job, err := p.sched.ScheduleOnce(ctx.Ctx, chatID, peerType, accessHash, when, scheduler.ActionMessage, text, ctx.SenderID())
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to schedule reminder: %v", err))
		return err
	}

	return ctx.EditOrReply(fmt.Sprintf("⏰ <b>Reminder set!</b>\nI will remind you in <code>%s</code>.\n<b>Job ID:</b> <code>#%d</code>", durStr, job.ID))
}

func (p *Plugin) handleSchedule(ctx *core.Context) error {
	if len(ctx.Args) < 2 {
		_ = ctx.EditOrReply("⚠️ Usage: <code>.schedule [in|every] &lt;duration&gt; &lt;text/command&gt;</code>\nExamples:\n• <code>.schedule in 30m .whois @user</code>\n• <code>.schedule every 2h .alive</code>")
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
		_ = ctx.EditOrReply("⚠️ Please provide a text or command payload to schedule.")
		return errors.New("missing schedule payload")
	}

	durStr := ctx.Args[durIdx]
	dur, err := scheduler.ParseDuration(durStr)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Invalid duration %q: %v", durStr, err))
		return err
	}

	prefixToStrip := ctx.Args[0] + " " + ctx.Args[1]
	if payloadIdx == 1 {
		prefixToStrip = ctx.Args[0]
	}
	payload := strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, prefixToStrip))
	if payload == "" {
		_ = ctx.EditOrReply("⚠️ Payload cannot be empty.")
		return errors.New("empty payload")
	}

	actionType := scheduler.ActionMessage
	if strings.HasPrefix(payload, ".") {
		actionType = scheduler.ActionCommand
	}

	chatID := getChatID(ctx)
	peerType, accessHash := extractPeerInfo(ctx.PeerID)
	if isRecurring {
		job, err := p.sched.ScheduleRecurring(ctx.Ctx, chatID, peerType, accessHash, dur, actionType, payload, ctx.SenderID())
		if err != nil {
			_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to create recurring schedule: %v", err))
			return err
		}
		return ctx.EditOrReply(fmt.Sprintf("📅 <b>Recurring schedule created!</b>\n<b>Interval:</b> every <code>%s</code>\n<b>Type:</b> <code>%s</code>\n<b>Action:</b> <code>%s</code>\n<b>Job ID:</b> <code>#%d</code>", durStr, actionType, html.EscapeString(payload), job.ID))
	}

	when := time.Now().Add(dur)
	job, err := p.sched.ScheduleOnce(ctx.Ctx, chatID, peerType, accessHash, when, actionType, payload, ctx.SenderID())
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to create schedule: %v", err))
		return err
	}
	return ctx.EditOrReply(fmt.Sprintf("📅 <b>Schedule created!</b>\n<b>Due in:</b> <code>%s</code>\n<b>Type:</b> <code>%s</code>\n<b>Action:</b> <code>%s</code>\n<b>Job ID:</b> <code>#%d</code>", durStr, actionType, html.EscapeString(payload), job.ID))
}

func (p *Plugin) handleList(ctx *core.Context) error {
	chatID := getChatID(ctx)
	jobs, err := p.sched.List(ctx.Ctx, chatID)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to list schedules: %v", err))
		return err
	}
	if len(jobs) == 0 {
		return ctx.EditOrReply("ℹ️ No active scheduled jobs in this chat.")
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
		status := j.Status
		if status == "" {
			status = "pending"
		}
		fmt.Fprintf(&sb, "• <b>#%d</b> [%s | %s | %s] <code>%s</code>\n  └ <i>Due in:</i> <code>%s</code>\n", j.ID, mode, j.ActionType, status, html.EscapeString(payloadSnippet), remaining)
		if j.LastError != "" {
			fmt.Fprintf(&sb, "  └ ⚠️ <i>Last Error (%d/%d attempts):</i> <code>%s</code>\n", j.AttemptCount, j.MaxAttempts, html.EscapeString(j.LastError))
		}
	}
	return ctx.EditOrReply(sb.String())
}

func (p *Plugin) handleCancel(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.EditOrReply("⚠️ Usage: <code>.cancelschedule &lt;id&gt;</code>")
		return errors.New("missing job id")
	}
	idStr := strings.TrimPrefix(ctx.Args[0], "#")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Invalid job ID %q: %v", idStr, err))
		return err
	}
	if err := p.sched.CancelScoped(ctx.Ctx, ctx.SenderID(), getChatID(ctx), id); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to cancel job #%d: %v", id, err))
		return err
	}
	return ctx.EditOrReply(fmt.Sprintf("🗑️ Scheduled job <code>#%d</code> canceled successfully.", id))
}

func (p *Plugin) handleSchedHistory(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.EditOrReply("⚠️ Usage: <code>.schedhistory &lt;id&gt; [limit]</code>")
		return errors.New("missing job id")
	}
	idStr := strings.TrimPrefix(ctx.Args[0], "#")
	jobID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Invalid job ID %q: %v", idStr, err))
		return err
	}
	limit := 10
	if len(ctx.Args) >= 2 {
		if n, err := strconv.Atoi(ctx.Args[1]); err == nil && n > 0 {
			if n > 50 { n = 50 }
			limit = n
		}
	}
	entries, err := p.sched.JobHistoryScoped(ctx.Ctx, ctx.SenderID(), getChatID(ctx), jobID, limit)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to fetch history for job #%d: %v", jobID, err))
		return err
	}
	if len(entries) == 0 {
		return ctx.EditOrReply(fmt.Sprintf("ℹ️ No execution history found for job <code>#%d</code>.", jobID))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "📋 <b>Execution History — Job #%d</b> (last %d):\n\n", jobID, len(entries))
	for _, e := range entries {
		icon := "✅"
		errPart := ""
		if !e.Success {
			icon = "❌"
			if e.ErrorMsg != "" {
				snippet := e.ErrorMsg
				if len(snippet) > 60 { snippet = snippet[:57] + "..." }
				errPart = fmt.Sprintf("\n  └ <i>Error:</i> <code>%s</code>", html.EscapeString(snippet))
			}
		}
		fmt.Fprintf(&sb, "%s <code>%s</code> — <i>%dms</i>%s\n", icon, e.RanAt.UTC().Format("2006-01-02 15:04:05"), e.DurationMs, errPart)
	}
	return ctx.EditOrReply(sb.String())
}

func getChatID(ctx *core.Context) int64 {
	if ctx.Chat != nil && ctx.Chat.ID != 0 { return ctx.Chat.ID }
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
