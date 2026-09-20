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
)

const (
	queueCapacity            = 256
	defaultWorkerIdleTimeout = 15 * time.Second
)

// Plugin manages user event logging (mentions, PMs, admin actions) to a dedicated Telegram destination.
type Plugin struct {
	svc           *userlog.Service
	ownerID       int64
	ownerUsername string

	mu                sync.RWMutex
	ctx               context.Context
	cancel            context.CancelFunc
	scope             *plugin.Scope
	eventBus          *core.EventBus
	subscriptions     []*core.Subscription
	queue             chan func()
	closing           bool
	shutdownDone      chan struct{}
	workerRunning     bool
	workerGeneration  uint64
	workerIdleTimeout time.Duration
	wg                sync.WaitGroup

	enqueuedCount  atomic.Int64
	deliveredCount atomic.Int64
	droppedCount   atomic.Int64
}

// New constructs a UserLog plugin without creating a lifecycle context or
// spawning workers. InitScope supplies both during managed registration.
func New(svc *userlog.Service, ownerID int64, ownerUsername ...string) *Plugin {
	username := ""
	if len(ownerUsername) > 0 {
		username = strings.TrimPrefix(ownerUsername[0], "@")
	}
	return &Plugin{
		svc:               svc,
		ownerID:           ownerID,
		ownerUsername:     username,
		workerIdleTimeout: defaultWorkerIdleTimeout,
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

// SetWorkerIdleTimeout configures how long the single lazy worker remains alive
// after the queue becomes idle. It is primarily useful for lifecycle tests.
func (p *Plugin) SetWorkerIdleTimeout(timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	p.mu.Lock()
	p.workerIdleTimeout = timeout
	p.mu.Unlock()
}

func (p *Plugin) lifecycleContext() context.Context {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closing {
		return nil
	}
	return p.ctx
}

// SetEventBus binds domain-event logging to the current plugin lifecycle.
// The EventBus reference is retained so disable -> enable can re-subscribe.
func (p *Plugin) SetEventBus(eb *core.EventBus) {
	p.mu.Lock()
	sameBinding := p.eventBus == eb && len(p.subscriptions) > 0
	p.eventBus = eb
	active := p.scope != nil && p.ctx != nil && !p.closing
	p.mu.Unlock()
	if active && !sameBinding {
		p.bindEventBus()
	}
}

func (p *Plugin) bindEventBus() {
	p.mu.Lock()
	eb := p.eventBus
	scope := p.scope
	active := eb != nil && scope != nil && p.ctx != nil && !p.closing
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

	add(eb.SubscribeOwned(owner, core.EventTypeAdminAction, func(event core.Event) {
		evt, ok := event.(*core.AdminActionEvent)
		if !ok || p.svc == nil {
			return
		}
		logCtx := p.lifecycleContext()
		if logCtx == nil {
			return
		}
		p.enqueue(func() {
			jobCtx, cancel := context.WithTimeout(logCtx, 15*time.Second)
			defer cancel()
			_ = p.svc.LogActionDetailed(
				jobCtx,
				evt.Action,
				evt.TargetID,
				evt.TargetName,
				evt.ActorID,
				evt.ChatTitle,
				evt.Reason,
				evt.Success,
				evt.Error,
			)
		})
	}))

	add(eb.SubscribeOwned(owner, core.EventTypePMPermit, func(event core.Event) {
		evt, ok := event.(*core.PMPermitEvent)
		if !ok || p.svc == nil {
			return
		}
		logCtx := p.lifecycleContext()
		if logCtx == nil {
			return
		}
		p.enqueue(func() {
			jobCtx, cancel := context.WithTimeout(logCtx, 15*time.Second)
			defer cancel()
			actionName := fmt.Sprintf("PMPermit %s", strings.ToUpper(evt.Action))
			_ = p.svc.LogActionDetailed(
				jobCtx,
				actionName,
				evt.UserID,
				evt.TargetName,
				0,
				"Private Message",
				evt.Reason,
				evt.Success,
				evt.Error,
			)
		})
	}))

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

func (p *Plugin) startWorkerLocked() error {
	if p.workerRunning {
		return nil
	}
	if p.scope == nil || p.queue == nil || p.ctx == nil || p.closing {
		return fmt.Errorf("userlog lifecycle is not active")
	}
	idleTimeout := p.workerIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = defaultWorkerIdleTimeout
	}
	queue := p.queue
	p.workerGeneration++
	generation := p.workerGeneration
	p.workerRunning = true
	p.wg.Add(1)
	if err := p.scope.Go(func(ctx context.Context) {
		p.worker(ctx, queue, idleTimeout, generation)
	}); err != nil {
		p.workerRunning = false
		p.wg.Done()
		return fmt.Errorf("start userlog worker: %w", err)
	}
	return nil
}

func (p *Plugin) worker(ctx context.Context, queue chan func(), idleTimeout time.Duration, generation uint64) {
	defer p.wg.Done()
	defer func() {
		p.mu.Lock()
		if p.queue == queue && p.workerGeneration == generation {
			p.workerRunning = false
		}
		p.mu.Unlock()
	}()

	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(idleTimeout)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-queue:
			if !ok {
				return
			}
			func() {
				defer func() { _ = recover() }()
				job()
			}()
			p.deliveredCount.Add(1)
			resetTimer()
		case <-timer.C:
			p.mu.Lock()
			if p.queue == queue && p.workerGeneration == generation && !p.closing && len(queue) == 0 {
				p.workerRunning = false
				p.mu.Unlock()
				return
			}
			p.mu.Unlock()
			timer.Reset(idleTimeout)
		}
	}
}

func (p *Plugin) enqueue(job func()) {
	if job == nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing || p.ctx == nil || p.scope == nil || p.queue == nil {
		p.droppedCount.Add(1)
		return
	}
	if !p.workerRunning {
		if err := p.startWorkerLocked(); err != nil {
			p.droppedCount.Add(1)
			return
		}
	}
	select {
	case p.queue <- job:
		p.enqueuedCount.Add(1)
	default:
		p.droppedCount.Add(1)
	}
}

// ShutdownContext stops new work, detaches event subscriptions, drains the
// bounded queue, and then cancels the lifecycle context.
func (p *Plugin) ShutdownContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	p.mu.Lock()
	if p.closing {
		done := p.shutdownDone
		p.mu.Unlock()
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

	p.closing = true
	done := make(chan struct{})
	p.shutdownDone = done
	subscriptions := append([]*core.Subscription(nil), p.subscriptions...)
	p.subscriptions = nil
	queue := p.queue
	cancel := p.cancel
	if queue != nil {
		close(queue)
	}
	p.mu.Unlock()

	for _, sub := range subscriptions {
		if sub != nil {
			sub.Close()
		}
	}

	go func() {
		p.wg.Wait()
		if cancel != nil {
			cancel()
		}
		p.mu.Lock()
		if p.shutdownDone == done {
			p.ctx = nil
			p.cancel = nil
			p.scope = nil
			p.queue = nil
			p.workerRunning = false
		}
		p.mu.Unlock()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		if cancel != nil {
			cancel()
		}
		return ctx.Err()
	}
}

var (
	_ plugin.ContextShutdowner         = (*Plugin)(nil)
	_ plugin.MessageEventRoutingPlugin = (*Plugin)(nil)
)

func (p *Plugin) Name() string { return "userlog" }

func (p *Plugin) Description() string {
	return "Forward tags, mentions, and new PMs to a dedicated log channel"
}

func (p *Plugin) Init() error { return nil }

// InitScope initializes a restartable lifecycle without spawning idle workers.
func (p *Plugin) InitScope(ctx context.Context, scope *plugin.Scope) error {
	if scope == nil {
		return fmt.Errorf("userlog plugin scope cannot be nil")
	}

	p.mu.Lock()
	if p.ctx != nil && !p.closing {
		p.mu.Unlock()
		return fmt.Errorf("userlog plugin lifecycle is already active")
	}
	p.scope = scope
	p.ctx, p.cancel = context.WithCancel(scope.Context())
	p.queue = make(chan func(), queueCapacity)
	p.closing = false
	p.shutdownDone = nil
	p.workerRunning = false
	if p.workerIdleTimeout <= 0 {
		p.workerIdleTimeout = defaultWorkerIdleTimeout
	}
	hasEventBus := p.eventBus != nil
	p.mu.Unlock()

	if hasEventBus {
		p.bindEventBus()
	}
	return nil
}

// MessageHookPriority returns priority for the message hook (Observability = 90).
func (p *Plugin) MessageHookPriority() int { return 90 }
func (p *Plugin) MessageHookRouting() core.MessageHookRouting {
	return core.MessageHookRouting{
		Lane: core.MessageHookEvent,
		Interests: []core.MessageHookInterest{
			{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerPrivate},
			{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerGroup | core.MessagePeerChannel, RequireMention: true},
		},
	}
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

	// Prevent recursive logging from the configured destination itself.
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
		name, id, text, logCtx := senderName, senderID, message.Text, p.lifecycleContext()
		p.enqueue(func() {
			jobCtx, cancel := context.WithTimeout(logCtx, 15*time.Second)
			defer cancel()
			_ = p.svc.LogPM(jobCtx, name, id, text)
		})
		return nil
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
	name, id, title, text, logCtx := senderName, senderID, chatTitle, message.Text, p.lifecycleContext()
	p.enqueue(func() {
		jobCtx, cancel := context.WithTimeout(logCtx, 15*time.Second)
		defer cancel()
		_ = p.svc.LogMention(jobCtx, title, name, id, text)
	})
	return nil
}
func (p *Plugin) handleSetLog(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.EditOrReply("⚠️ UserLog service is not configured.")
	}

	var dest userlog.LogDestination

	if len(ctx.Args) > 0 {
		targetRef := ctx.Args[0]
		if ctx.Resolver == nil {
			return ctx.EditOrReply("⚠️ Peer resolver is not initialized.")
		}
		resolved, err := ctx.Resolver.ResolveChat(ctx.Ctx, targetRef)
		if err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Could not resolve %q: %v", targetRef, err))
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
			return ctx.EditOrReply("⚠️ Target must be a group or channel.")
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
			return ctx.EditOrReply("⚠️ Please run <code>.setlog</code> inside a group/channel or specify a target: <code>.setlog @channel</code>.")
		}
	}

	// Verification: send a test verification message to ensure we have permission to write
	if ctx.Svc != nil {
		testMsg := "🧪 <b>UserLog Destination Verification</b>\n\n• <b>Status:</b> Verified\n• <b>System:</b> GoUltroid Audit Logging\n• <b>Time:</b> <code>" + time.Now().UTC().Format(time.RFC3339) + "</code>"
		_, err := ctx.Svc.SendMessage(ctx.Ctx, dest.InputPeer(), testMsg)
		if err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ <b>Verification failed:</b> Cannot post to target (%v). Make sure the bot/account has permission to post.", err))
		}
	}

	if err := p.svc.SetDestination(ctx.Ctx, dest); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to save log destination: %v", err))
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
		return ctx.EditOrReply("⚠️ UserLog service is not configured.")
	}

	if len(ctx.Args) == 1 {
		arg := strings.ToLower(ctx.Args[0])
		if arg == "test" {
			latency, err := p.svc.SendTestMessage(ctx.Ctx)
			if err != nil {
				return ctx.EditOrReply(fmt.Sprintf("❌ <b>Log test failed:</b> %v", err))
			}
			return ctx.EditOrReply(fmt.Sprintf("✅ <b>UserLog test successful!</b>\n\n• <b>Latency:</b> %s\n• <b>Destination:</b> Verified", latency.Round(time.Millisecond)))
		}
		if arg == "clear" || arg == "disable" || arg == "off" {
			if err := p.svc.ClearDestination(ctx.Ctx); err != nil {
				return ctx.EditOrReply(fmt.Sprintf("❌ Failed to clear log destination: %v", err))
			}
			return ctx.EditOrReply("✅ <b>Log destination disabled.</b> No logs will be sent.")
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
			return ctx.EditOrReply("⚠️ Unknown category. Choose <code>tags</code>, <code>pms</code>, or <code>actions</code>.")
		}
		if err := p.svc.SetFeatureEnabled(ctx.Ctx, settingKey, enable); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Failed to update setting: %v", err))
		}
		status := "DISABLED"
		if enable {
			status = "ENABLED"
		}
		return ctx.EditOrReply(fmt.Sprintf("✅ Logging for <code>%s</code> is now <b>%s</b>.", category, status))
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
			"• <b>Queue Stats:</b> Enqueued: <code>%d</code> | Delivered: <code>%d</code> | Dropped: <code>%d</code>\n\n"+
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
		p.enqueuedCount.Load(),
		p.deliveredCount.Load(),
		p.droppedCount.Load(),
		tagsStr,
		pmsStr,
		actionsStr,
	)
	return ctx.EditOrReply(text)
}
