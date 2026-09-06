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
	asyncLogWorkers = 16
	queueCapacity   = 256
)

// Plugin manages user event logging (mentions, PMs, admin actions) to a dedicated Telegram destination.
type Plugin struct {
	svc           *userlog.Service
	ownerID       int64
	ownerUsername string
	queue         chan func()
	closing       atomic.Bool
	mu            sync.RWMutex
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	once          sync.Once

	enqueuedCount  atomic.Int64
	deliveredCount atomic.Int64
	droppedCount   atomic.Int64
}

// New creates an initialized UserLog plugin with a bounded, thread-safe asynchronous worker queue.
func New(svc *userlog.Service, ownerID int64, ownerUsername ...string) *Plugin {
	ctx, cancel := context.WithCancel(context.Background())
	username := ""
	if len(ownerUsername) > 0 {
		username = strings.TrimPrefix(ownerUsername[0], "@")
	}
	p := &Plugin{
		svc:           svc,
		ownerID:       ownerID,
		ownerUsername: username,
		queue:         make(chan func(), queueCapacity),
		ctx:           ctx,
		cancel:        cancel,
	}
	p.wg.Add(asyncLogWorkers)
	for i := 0; i < asyncLogWorkers; i++ {
		go p.worker()
	}
	return p
}

// SetOwnerUsername configures the owner's Telegram username for @username mention detection.
func (p *Plugin) SetOwnerUsername(username string) {
	p.ownerUsername = strings.TrimPrefix(username, "@")
}

// SetEventBus subscribes the plugin to domain events (AdminActionEvent and PMPermitEvent).
func (p *Plugin) SetEventBus(eb *core.EventBus) {
	if eb == nil {
		return
	}
	eb.Subscribe(core.EventTypeAdminAction, func(event core.Event) {
		if evt, ok := event.(*core.AdminActionEvent); ok && p.svc != nil {
			logCtx := p.ctx
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
		}
	})
	eb.Subscribe(core.EventTypePMPermit, func(event core.Event) {
		if evt, ok := event.(*core.PMPermitEvent); ok && p.svc != nil {
			logCtx := p.ctx
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
		}
	})
}

func (p *Plugin) worker() {
	defer p.wg.Done()
	for job := range p.queue {
		func() {
			defer func() { _ = recover() }()
			job()
		}()
		p.deliveredCount.Add(1)
	}
}

func (p *Plugin) enqueue(job func()) {
	if job == nil {
		return
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closing.Load() {
		return
	}
	select {
	case p.queue <- job:
		p.enqueuedCount.Add(1)
	default:
		p.droppedCount.Add(1)
	}
}

// ShutdownContext stops accepting new work, drains the bounded queue, and cancels worker context.
func (p *Plugin) ShutdownContext(ctx context.Context) error {
	p.mu.Lock()
	p.closing.Store(true)
	p.once.Do(func() {
		close(p.queue)
	})
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		p.cancel()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ plugin.ContextShutdowner = (*Plugin)(nil)

func (p *Plugin) Name() string { return "userlog" }

func (p *Plugin) Description() string {
	return "Forward tags, mentions, and new PMs to a dedicated log channel"
}

func (p *Plugin) Init() error { return nil }

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
func (p *Plugin) HandleIncomingMessage(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error {
	if p.svc == nil || msg == nil || msg.Out {
		return nil
	}

	// Filter out messages occurring inside the log destination chat itself (prevent loop)
	if p.svc.IsLogDestination(msg.PeerID) {
		return nil
	}

	senderID := int64(0)
	senderName := "Unknown User"
	isBot := false
	if msg.FromID != nil {
		if u, ok := msg.FromID.(*tg.PeerUser); ok {
			senderID = u.UserID
			if userObj, exists := e.Users[senderID]; exists && userObj != nil {
				senderName = strings.TrimSpace(userObj.FirstName + " " + userObj.LastName)
				if senderName == "" {
					senderName = userObj.Username
				}
				if senderName == "" {
					senderName = fmt.Sprintf("User %d", senderID)
				}
				isBot = userObj.Bot
			}
		}
	}

	// Filter bot senders
	if isBot {
		return nil
	}

	// 1. PM Handling
	if peerUser, ok := msg.PeerID.(*tg.PeerUser); ok {
		if senderID == 0 {
			senderID = peerUser.UserID
		}
		// Ignore Telegram service notification bots (777000) or self
		if senderID == 777000 || senderID == p.ownerID {
			return nil
		}
		name, id, text, logCtx := senderName, senderID, msg.Message, p.ctx
		p.enqueue(func() {
			jobCtx, cancel := context.WithTimeout(logCtx, 15*time.Second)
			defer cancel()
			_ = p.svc.LogPM(jobCtx, name, id, text)
		})
		return nil
	}

	// 2. Mention Handling
	isMentioned := msg.Mentioned
	if !isMentioned {
		runes := []rune(msg.Message)
		for _, ent := range msg.Entities {
			switch m := ent.(type) {
			case *tg.MessageEntityMentionName:
				if m.UserID == p.ownerID {
					isMentioned = true
					break
				}
			case *tg.MessageEntityMention:
				if p.ownerUsername != "" && m.Offset >= 0 && m.Offset+m.Length <= len(runes) {
					mentionText := string(runes[m.Offset : m.Offset+m.Length])
					mentionClean := strings.TrimPrefix(strings.ToLower(mentionText), "@")
					if mentionClean == strings.ToLower(p.ownerUsername) {
						isMentioned = true
						break
					}
				}
			}
			if isMentioned {
				break
			}
		}
	}

	if !isMentioned {
		return nil
	}

	chatTitle := "Group Chat"
	switch c := msg.PeerID.(type) {
	case *tg.PeerChat:
		if chatObj, exists := e.Chats[c.ChatID]; exists && chatObj != nil {
			chatTitle = chatObj.Title
		}
	case *tg.PeerChannel:
		if chObj, exists := e.Channels[c.ChannelID]; exists && chObj != nil {
			chatTitle = chObj.Title
		}
	}

	name, id, title, text, logCtx := senderName, senderID, chatTitle, msg.Message, p.ctx
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
