package userlog

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/userlog"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	userLogExecutionTimeout = 15 * time.Second
	userLogOrderingKey      = "msg-event:plugin:userlog"
)

// Plugin manages user event logging (mentions, PMs, admin actions) to a dedicated Telegram destination.
type Plugin struct {
	svc           *userlog.Service
	ownerID       int64
	ownerUsername string

	mu            sync.RWMutex
	scope         *plugin.Scope
	eventBus      *core.EventBus
	subscriptions []*core.Subscription
	closing       bool
	tasks         tasks.Client
	effectSeq     atomic.Uint64
}

// New constructs a UserLog plugin without creating a lifecycle context or
// spawning workers. InitScope supplies both during managed registration.
func New(svc *userlog.Service, ownerID int64, ownerUsername ...string) *Plugin {
	username := ""
	if len(ownerUsername) > 0 {
		username = strings.TrimPrefix(ownerUsername[0], "@")
	}
	return &Plugin{
		svc:           svc,
		ownerID:       ownerID,
		ownerUsername: username,
	}
}

// SetOwnerUsername configures the owner's Telegram username for @username mention detection.
func (p *Plugin) SetOwnerUsername(username string) {
	p.mu.Lock()
	p.ownerUsername = strings.TrimPrefix(username, "@")
	p.mu.Unlock()
}

func (p *Plugin) ownerUsernameValue() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ownerUsername
}

// SetWorkerIdleTimeout is retained for source compatibility. UserLog no longer
// owns a private worker, so the value has no runtime effect.
func (p *Plugin) SetWorkerIdleTimeout(time.Duration) {}

// SetEventBus binds domain-event logging to the current plugin lifecycle.
// The EventBus reference is retained so disable -> enable can re-subscribe.
func (p *Plugin) SetEventBus(eb *core.EventBus) {
	p.mu.Lock()
	sameBinding := p.eventBus == eb && len(p.subscriptions) > 0
	p.eventBus = eb
	active := p.scope != nil && !p.closing
	p.mu.Unlock()
	if active && !sameBinding {
		p.bindEventBus()
	}
}

func (p *Plugin) bindEventBus() {
	p.mu.Lock()
	eb := p.eventBus
	scope := p.scope
	active := eb != nil && scope != nil && !p.closing
	old := append([]*core.Subscription(nil), p.subscriptions...)
	p.subscriptions = nil
	p.mu.Unlock()

	for _, sub := range old {
		if sub != nil {
			sub.Close()
		}
	}
	if !active {
		return
	}

	owner := scope.Owner()
	scopeID := tasks.ScopeIdentity{Owner: owner, Generation: scope.Generation()}
	var created []*core.Subscription
	add := func(sub *core.Subscription) {
		if sub == nil {
			return
		}
		created = append(created, sub)
		if err := scope.Defer(sub.Close); err != nil {
			sub.Close()
		}
	}

	add(eb.SubscribeWithOptions(
		core.EventTypeAdminAction,
		func(ctx context.Context, event core.Event) error {
			evt, ok := event.(*core.AdminActionEvent)
			if !ok || p.svc == nil {
				return nil
			}
			action, targetID, targetName := evt.Action, evt.TargetID, evt.TargetName
			actorID, chatTitle, reason := evt.ActorID, evt.ChatTitle, evt.Reason
			success, errText := evt.Success, evt.Error
			return p.submitDomainLog(ctx, "admin", fmt.Sprintf("%s:%d:%d", action, targetID, actorID), func(taskCtx context.Context) error {
				return p.svc.LogActionDetailed(taskCtx, action, targetID, targetName, actorID, chatTitle, reason, success, errText)
			})
		},
		core.SubscribeOptions{
			Owner:       owner,
			Scope:       scopeID,
			Timeout:     userLogExecutionTimeout,
			MinPriority: core.PriorityLow,
		},
	))

	add(eb.SubscribeWithOptions(
		core.EventTypePMPermit,
		func(ctx context.Context, event core.Event) error {
			evt, ok := event.(*core.PMPermitEvent)
			if !ok || p.svc == nil {
				return nil
			}
			actionName := fmt.Sprintf("PMPermit %s", strings.ToUpper(evt.Action))
			userID, targetName := evt.UserID, evt.TargetName
			reason, success, errText := evt.Reason, evt.Success, evt.Error
			return p.submitDomainLog(ctx, "pmpermit", fmt.Sprintf("%s:%d", evt.Action, userID), func(taskCtx context.Context) error {
				return p.svc.LogActionDetailed(taskCtx, actionName, userID, targetName, 0, "Private Message", reason, success, errText)
			})
		},
		core.SubscribeOptions{
			Owner:       owner,
			Scope:       scopeID,
			Timeout:     userLogExecutionTimeout,
			MinPriority: core.PriorityLow,
		},
	))

	p.mu.Lock()
	stale := p.scope != scope || p.eventBus != eb || p.closing
	if !stale {
		p.subscriptions = append(p.subscriptions, created...)
	}
	p.mu.Unlock()
	if stale {
		for _, sub := range created {
			sub.Close()
		}
	}
}

func (p *Plugin) getTaskClient() tasks.Client {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.tasks
}

func (p *Plugin) submitDomainLog(ctx context.Context, kind, input string, handler func(context.Context) error) error {
	if handler == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client := p.getTaskClient()
	if client == nil {
		// Compatibility for direct/manual plugin construction. Production managed
		// registration always supplies the scoped TaskClient through InitPlugin.
		return handler(ctx)
	}
	_, err := client.Submit(ctx, tasks.WorkSpec{
		ID:               tasks.TaskID(fmt.Sprintf("userlog:%s:%d", kind, p.effectSeq.Add(1))),
		Pool:             tasks.PoolID("general"),
		Class:            tasks.PriorityNormal,
		OrderingKey:      userLogOrderingKey,
		ExecutionTimeout: userLogExecutionTimeout,
		Input:            input,
		Handler:          handler,
	})
	if err != nil {
		return fmt.Errorf("userlog: submit %s log effect: %w", kind, err)
	}
	return nil
}

// ShutdownContext stops new work and detaches EventBus subscriptions.
// In-flight message/EventBus tasks are generation-scoped by the shared
// TaskEngine and are cancelled by plugin-manager scope cancellation.
func (p *Plugin) ShutdownContext(ctx context.Context) error {
	_ = ctx
	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		return nil
	}
	p.closing = true
	subscriptions := append([]*core.Subscription(nil), p.subscriptions...)
	p.subscriptions = nil
	p.scope = nil
	p.tasks = nil
	p.mu.Unlock()

	for _, sub := range subscriptions {
		if sub != nil {
			sub.Close()
		}
	}
	return nil
}

var (
	_ plugin.ContextShutdowner               = (*Plugin)(nil)
	_ plugin.PluginContextInitializer        = (*Plugin)(nil)
	_ plugin.MessageEventRegistrationsPlugin = (*Plugin)(nil)
)

func (p *Plugin) Name() string { return "userlog" }

func (p *Plugin) Description() string {
	return "Forward tags, mentions, and new PMs to a dedicated log channel"
}

func (p *Plugin) Init() error { return nil }

func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	if pctx == nil || pctx.Scope() == nil {
		return fmt.Errorf("userlog plugin context/scope cannot be nil")
	}
	client, err := pctx.TaskClient()
	if err != nil {
		return fmt.Errorf("userlog: initialize task client: %w", err)
	}
	return p.initScope(pctx.Scope(), client)
}

// InitScope initializes the generation-owned lifecycle without spawning
// plugin-private workers. All asynchronous delivery belongs to dispatcher or
// EventBus TaskEngine execution.
func (p *Plugin) InitScope(ctx context.Context, scope *plugin.Scope) error {
	return p.initScope(scope, nil)
}

func (p *Plugin) initScope(scope *plugin.Scope, client tasks.Client) error {
	if scope == nil {
		return fmt.Errorf("userlog plugin scope cannot be nil")
	}

	p.mu.Lock()
	if p.scope != nil && !p.closing {
		p.mu.Unlock()
		return fmt.Errorf("userlog plugin lifecycle is already active")
	}
	p.scope = scope
	p.tasks = client
	p.closing = false
	hasEventBus := p.eventBus != nil
	p.mu.Unlock()

	if hasEventBus {
		p.bindEventBus()
	}
	return nil
}

// MessageHookPriority returns priority for observability work.
func (p *Plugin) MessageHookPriority() int { return 90 }

func (p *Plugin) MessageHookRegistrations() []core.MessageHookRegistration {
	return []core.MessageHookRegistration{{
		Priority: p.MessageHookPriority(),
		Routing: core.MessageHookRouting{
			Lane: core.MessageHookEvent,
			Interests: []core.MessageHookInterest{
				{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerPrivate},
				{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerGroup | core.MessagePeerChannel, RequireMention: true},
			},
		},
		Execution: core.MessageHookExecutionPolicy{
			FailurePolicy:  core.MessageHookFailOpen,
			HandlerTimeout: userLogExecutionTimeout,
			TaskTimeout:    userLogExecutionTimeout,
			Ordering:       core.MessageHookOrderingPlugin,
		},
		Handler: p.HandleMessageEvent,
	}}
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name: "setlog", Aliases: []string{"setlogchat"},
			Description: "Set the current chat or specified channel as the log destination",
			Usage:       ".setlog [@channel / -100...]", Category: "Admin", Permission: core.PermissionOwner,
			Handler: p.handleSetLog,
		},
		{
			Name: "log", Aliases: []string{"logstatus"},
			Description: "View health dashboard, run tests, or toggle log categories",
			Usage:       ".log [status | test | clear | tags|pms|actions on|off]", Category: "Admin", Permission: core.PermissionOwner,
			Handler: p.handleLogStatus,
		},
	}
}

// HandleIncomingMessage inspects updates for PMs or mentions and dispatches to the queue.
func (p *Plugin) HandleMessageEvent(ctx context.Context, message *core.MessageEnvelope) error {
	if p.svc == nil || message == nil || message.Outgoing {
		return nil
	}

	if p.svc.IsLogDestinationRef(message.Peer) {
		return nil
	}

	senderID := message.Sender.ID
	if senderID == 0 && message.IsPrivate() {
		senderID = message.ChatID
	}
	if message.Sender.IsBot {
		return nil
	}
	senderName := message.SenderName()

	if message.IsPrivate() {
		if senderID == 777000 || senderID == p.ownerID {
			return nil
		}
		return p.svc.LogPM(ctx, senderName, senderID, message.Text)
	}

	isMentioned := message.Mentioned ||
		message.MentionsUser(p.ownerID) ||
		message.MentionsUsername(p.ownerUsernameValue())
	if !isMentioned {
		return nil
	}

	chatTitle := message.Chat.Title
	if chatTitle == "" {
		chatTitle = "Group Chat"
	}
	return p.svc.LogMention(ctx, chatTitle, senderName, senderID, message.Text)
}

func (p *Plugin) handleSetLog(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Status("UserLog service is not configured.")
	}

	var dest userlog.LogDestination

	if len(ctx.Args) > 0 {
		targetRef := ctx.Args[0]
		if ctx.Resolver == nil {
			return ctx.Status("Peer resolver is not initialized.")
		}
		resolved, err := ctx.Resolver.ResolveChat(ctx.Ctx, targetRef)
		if err != nil {
			return ctx.Fail(err, fmt.Sprintf("Could not resolve %q.", core.EscapeHTML(targetRef)))
		}
		switch peer := resolved.(type) {
		case *tg.InputPeerChannel:
			dest = userlog.LogDestination{
				Type:       userlog.LogDestinationChannel,
				ID:         peer.ChannelID,
				AccessHash: peer.AccessHash,
				Title:      targetRef,
			}
		case *tg.InputPeerChat:
			dest = userlog.LogDestination{
				Type:  userlog.LogDestinationChat,
				ID:    peer.ChatID,
				Title: targetRef,
			}
		default:
			return ctx.Status("Target must be a group or channel.")
		}
	} else {
		switch peer := ctx.PeerID.(type) {
		case *tg.InputPeerChat:
			title := "Group Chat"
			if ctx.Chat != nil && ctx.Chat.Title != "" {
				title = ctx.Chat.Title
			}
			dest = userlog.LogDestination{
				Type:  userlog.LogDestinationChat,
				ID:    peer.ChatID,
				Title: title,
			}
		case *tg.InputPeerChannel:
			title := "Channel / Supergroup"
			if ctx.Chat != nil && ctx.Chat.Title != "" {
				title = ctx.Chat.Title
			}
			dest = userlog.LogDestination{
				Type:       userlog.LogDestinationChannel,
				ID:         peer.ChannelID,
				AccessHash: peer.AccessHash,
				Title:      title,
			}
		default:
			return ctx.Status("Please run <code>.setlog</code> inside a group/channel or specify a target: <code>.setlog @channel</code>.")
		}
	}

	// Verification: send a test verification message to ensure we have permission to write.
	messageSvc := ctx.MessageActionService()
	if messageSvc == nil {
		return ctx.Error("<b>Verification failed:</b> Telegram message service is unavailable.")
	}
	testMsg := "🧪 <b>UserLog Destination Verification</b>\n\n• <b>Status:</b> Verified\n• <b>System:</b> GoUltroid Audit Logging\n• <b>Time:</b> <code>" + time.Now().UTC().Format(time.RFC3339) + "</code>"
	if _, err := messageSvc.SendMessage(ctx.Ctx, dest.InputPeer(), testMsg); err != nil {
		return ctx.Fail(err, "<b>Verification failed:</b> Cannot post to target. Make sure the bot/account has permission to post.")
	}

	if err := p.svc.SetDestination(ctx.Ctx, dest); err != nil {
		return ctx.Fail(err, "Failed to save log destination.")
	}

	destType := "group"
	if dest.Type == userlog.LogDestinationChannel {
		destType = "channel/supergroup"
	}
	return ctx.EditOrReply(fmt.Sprintf(
		"✅ <b>Log destination verified & active!</b>\n\n"+
			"• <b>Title:</b> %s\n"+
			"• <b>Type:</b> %s\n"+
			"• <b>ID:</b> <code>%d</code>",
		core.EscapeHTML(dest.Title), destType, dest.ID,
	))
}

func (p *Plugin) handleLogStatus(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Status("UserLog service is not configured.")
	}

	if len(ctx.Args) == 1 {
		arg := strings.ToLower(ctx.Args[0])
		if arg == "test" {
			latency, err := p.svc.SendTestMessage(ctx.Ctx)
			if err != nil {
				return ctx.Fail(err, "<b>Log test failed.</b>")
			}
			return ctx.Success(fmt.Sprintf("<b>UserLog test successful!</b>\n\n• <b>Latency:</b> %s\n• <b>Destination:</b> Verified", latency.Round(time.Millisecond)))
		}
		if arg == "clear" || arg == "disable" || arg == "off" {
			if err := p.svc.ClearDestination(ctx.Ctx); err != nil {
				return ctx.Fail(err, "Failed to clear log destination.")
			}
			return ctx.Success("<b>Log destination disabled.</b> No logs will be sent.")
		}
	}

	if len(ctx.Args) >= 2 {
		category := strings.ToLower(ctx.Args[0])
		action := strings.ToLower(ctx.Args[1])
		enable := action == "on" || action == "enable" || action == "true"
		var settingKey string
		switch category {
		case "tags", "tag", "mentions":
			settingKey = userlog.SettingTagsEnable
		case "pms", "pm", "dms":
			settingKey = userlog.SettingPMsEnable
		case "actions", "action", "admin":
			settingKey = userlog.SettingActionsEnable
		default:
			return ctx.Status("Unknown category. Choose <code>tags</code>, <code>pms</code>, or <code>actions</code>.")
		}
		if err := p.svc.SetFeatureEnabled(ctx.Ctx, settingKey, enable); err != nil {
			return ctx.Fail(err, "Failed to update log setting.")
		}
		status := "DISABLED"
		if enable {
			status = "ENABLED"
		}
		return ctx.Success(fmt.Sprintf("Logging for <code>%s</code> is now <b>%s</b>.", category, status))
	}

	// Health dashboard display
	dest, _ := p.svc.GetDestination(ctx.Ctx)
	stats := p.svc.Stats(ctx.Ctx)
	tagsOn, _ := p.svc.IsFeatureEnabled(ctx.Ctx, userlog.SettingTagsEnable)
	pmsOn, _ := p.svc.IsFeatureEnabled(ctx.Ctx, userlog.SettingPMsEnable)
	actionsOn, _ := p.svc.IsFeatureEnabled(ctx.Ctx, userlog.SettingActionsEnable)

	destStr := "<i>Not configured (use .setlog)</i>"
	if dest != nil && dest.ID != 0 {
		title := dest.Title
		if title == "" {
			title = string(dest.Type)
		}
		destStr = fmt.Sprintf("<b>%s</b> (<code>%d</code>)", core.EscapeHTML(title), dest.ID)
	}

	statusBadge := "⚪ UNCONFIGURED"
	switch stats.Status {
	case userlog.StatusHealthy:
		statusBadge = "🟢 HEALTHY"
	case userlog.StatusDegraded:
		statusBadge = "🟡 DEGRADED"
	case userlog.StatusFailed:
		statusBadge = "🔴 FAILED"
	}

	tagsStr := "❌ Disabled"
	if tagsOn {
		tagsStr = "✅ Enabled"
	}
	pmsStr := "❌ Disabled"
	if pmsOn {
		pmsStr = "✅ Enabled"
	}
	actionsStr := "❌ Disabled"
	if actionsOn {
		actionsStr = "✅ Enabled"
	}

	text := fmt.Sprintf(
		"📋 <b>UserLog Dashboard</b>\n\n"+
			"• <b>Status:</b> %s\n"+
			"• <b>Destination:</b> %s\n"+
			"• <b>Delivery Stats:</b> Delivered: <code>%d</code> | Failed: <code>%d</code> | Consecutive failures: <code>%d</code>\n\n"+
			"<b>Categories:</b>\n"+
			"• <b>Mentions/Tags:</b> %s\n"+
			"• <b>Private Messages:</b> %s\n"+
			"• <b>Admin Actions:</b> %s\n\n"+
			"<b>Commands:</b>\n"+
			"• <code>.log test</code> — Test delivery\n"+
			"• <code>.log [tags|pms|actions] [on|off]</code> — Toggle category\n"+
			"• <code>.log clear</code> — Disable log destination",
		statusBadge,
		destStr,
		stats.DeliveredCount,
		stats.FailedCount,
		stats.ConsecutiveFailures,
		tagsStr,
		pmsStr,
		actionsStr,
	)
	return ctx.EditOrReply(text)
}
