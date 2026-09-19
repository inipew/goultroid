package afk

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/plugin"
	"go.uber.org/zap"
)

var (
	_ plugin.MessageEventRoutingPlugin = (*Plugin)(nil)
	_ plugin.ContextInitializer        = (*Plugin)(nil)
	_ execution.CapabilityProvider     = (*Plugin)(nil)
)

const defaultWelcomeDeleteDelay = 2 * time.Second

type afkState struct {
	isAFK  bool
	reason string
	since  time.Time
}

type Plugin struct {
	db                 Repository
	ownerID            int64
	ownerUsername      string
	svcFunc            func() core.TelegramServicer
	resolver           core.PeerResolver
	logger             *zap.Logger
	welcomePrivateOnly bool
	welcomeDeleteDelay time.Duration
	stateMu            sync.RWMutex
	state              atomic.Pointer[afkState]
	autoReply          atomic.Bool
	transitionMu       sync.Mutex
	cooldownMu         sync.Mutex
	cooldownMap        map[[2]int64]time.Time
	cooldownDur        time.Duration
}

func New(db Repository, ownerID int64, svcFunc func() core.TelegramServicer) *Plugin {
	p := &Plugin{
		db:                 db,
		ownerID:            ownerID,
		svcFunc:            svcFunc,
		cooldownMap:        make(map[[2]int64]time.Time),
		cooldownDur:        60 * time.Second,
		welcomePrivateOnly: true,
		welcomeDeleteDelay: defaultWelcomeDeleteDelay,
	}
	p.state.Store(&afkState{isAFK: false})
	p.autoReply.Store(true)
	return p
}

func (p *Plugin) Name() string { return "afk" }
func (p *Plugin) Description() string {
	return "Away From Keyboard status manager and intelligent auto-reply system"
}
func (p *Plugin) SetOwnerUsername(username string) {
	p.stateMu.Lock()
	p.ownerUsername = strings.TrimPrefix(username, "@")
	p.stateMu.Unlock()
}
func (p *Plugin) getOwnerUsername() string {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()
	return p.ownerUsername
}
func (p *Plugin) SetResolver(resolver core.PeerResolver) {
	p.stateMu.Lock()
	p.resolver = resolver
	p.stateMu.Unlock()
}
func (p *Plugin) getResolver() core.PeerResolver {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()
	return p.resolver
}
func (p *Plugin) SetLogger(logger *zap.Logger) {
	p.stateMu.Lock()
	p.logger = logger
	p.stateMu.Unlock()
}
func (p *Plugin) getLogger() *zap.Logger {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()
	return p.logger
}
func (p *Plugin) SetWelcomePrivateOnly(privateOnly bool) {
	p.stateMu.Lock()
	p.welcomePrivateOnly = privateOnly
	p.stateMu.Unlock()
}
func (p *Plugin) isWelcomePrivateOnly() bool {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()
	return p.welcomePrivateOnly
}

// SetWelcomeDeleteDelay configures how long a welcome-back message remains visible.
// A non-positive delay disables automatic deletion.
func (p *Plugin) SetWelcomeDeleteDelay(delay time.Duration) {
	p.stateMu.Lock()
	p.welcomeDeleteDelay = delay
	p.stateMu.Unlock()
}
func (p *Plugin) getWelcomeDeleteDelay() time.Duration {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()
	return p.welcomeDeleteDelay
}
func (p *Plugin) SetAutoReply(enabled bool) {
	p.autoReply.Store(enabled)
}

func (p *Plugin) AutoReplyEnabled() bool {
	return p.autoReply.Load()
}

func (p *Plugin) SetCooldown(duration time.Duration) {
	if duration < 0 {
		return
	}
	p.cooldownMu.Lock()
	p.cooldownDur = duration
	if duration == 0 {
		p.cooldownMap = make(map[[2]int64]time.Time)
	}
	p.cooldownMu.Unlock()
}
func (p *Plugin) InitContext(ctx context.Context) error { return p.loadState(ctx) }
func (p *Plugin) Init() error {
	return p.validateDependencies()
}

func (p *Plugin) validateDependencies() error { return nil }

func (p *Plugin) loadState(ctx context.Context) error {
	if p.db == nil || p.ownerID == 0 {
		return nil
	}
	status, err := p.db.GetAFK(ctx, p.ownerID)
	if err != nil {
		if logger := p.getLogger(); logger != nil {
			logger.Error("failed to load initial AFK status from database", zap.Error(err))
		}
		return fmt.Errorf("load AFK state: %w", err)
	}
	if status != nil && status.IsAFK {
		p.state.Store(&afkState{isAFK: true, reason: status.Reason, since: status.Since})
	} else {
		p.state.Store(&afkState{isAFK: false})
	}
	return nil
}

func (p *Plugin) MessageHookPriority() int { return 50 }
func (p *Plugin) MessageHookRouting() core.MessageHookRouting {
	return core.MessageHookRouting{
		Lane: core.MessageHookEvent,
		Interests: []core.MessageHookInterest{
			{Directions: core.MessageDirectionOutgoing, Peers: core.MessagePeerStable},
			{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerPrivate},
			{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerGroup | core.MessagePeerChannel, RequireMention: true},
			{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerGroup | core.MessagePeerChannel, RequireReply: true},
		},
	}
}
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{{ID: "afk", Name: "AFK", Description: "Away From Keyboard status manager and intelligent auto-reply system", Category: "Utility", Surfaces: execution.SurfaceUserbot}}
}
func (p *Plugin) Commands() []core.Command {
	return []core.Command{{Name: "afk", Description: "Set AFK status with an optional reason, toggle, or off", Usage: ".afk [on|off|status|toggle|reason]", Category: "AFK", Permission: core.PermissionOwner, Surfaces: execution.SurfaceUserbot, Handler: p.handleAFKCommand}}
}

func (p *Plugin) handleAFKCommand(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		if st := p.state.Load(); st != nil && st.isAFK {
			dur, changed, err := p.disableAFK(ctx.Ctx)
			if err != nil {
				return ctx.EditOrReply(fmt.Sprintf("❌ Failed to deactivate AFK mode: %v", err))
			}
			if !changed {
				return ctx.EditOrReply("ℹ️ <b>AFK Mode is already inactive.</b>")
			}
			return ctx.EditOrReply(fmt.Sprintf("☀️ <b>AFK Mode Deactivated!</b>\n<b>Away for:</b> <code>%s</code>", dur))
		}
		const r = "Away from keyboard"
		if err := p.enableAFK(ctx.Ctx, r); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Failed to activate AFK mode: %v", err))
		}
		return ctx.EditOrReply(fmt.Sprintf("🌙 <b>AFK Mode Activated!</b>\n<b>Reason:</b> <i>%s</i>", html.EscapeString(r)))
	}
	sub := strings.ToLower(ctx.Args[0])
	switch sub {
	case "off", "disable", "stop":
		dur, changed, err := p.disableAFK(ctx.Ctx)
		if err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Failed to deactivate AFK mode: %v", err))
		}
		if !changed {
			return ctx.EditOrReply("ℹ️ <b>AFK Mode is already inactive.</b>")
		}
		return ctx.EditOrReply(fmt.Sprintf("☀️ <b>AFK Mode Deactivated!</b>\n<b>Away for:</b> <code>%s</code>", dur))
	case "status":
		st := p.state.Load()
		if st != nil && st.isAFK {
			return ctx.EditOrReply(fmt.Sprintf("🌙 <b>AFK Status: Active</b>\n<b>Reason:</b> <i>%s</i>\n<b>Since:</b> <code>%s ago</code>", html.EscapeString(st.reason), formatDuration(time.Since(st.since))))
		}
		return ctx.EditOrReply("🟢 <b>AFK Status: Inactive</b>")
	case "on":
		reason := "Away from keyboard"
		if len(ctx.Args) > 1 {
			reason = strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, ctx.Args[0]))
		}
		if err := p.enableAFK(ctx.Ctx, reason); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Failed to activate AFK mode: %v", err))
		}
		return ctx.EditOrReply(fmt.Sprintf("🌙 <b>AFK Mode Activated!</b>\n<b>Reason:</b> <i>%s</i>", html.EscapeString(reason)))
	case "toggle":
		if st := p.state.Load(); st != nil && st.isAFK {
			dur, changed, err := p.disableAFK(ctx.Ctx)
			if err != nil {
				return ctx.EditOrReply(fmt.Sprintf("❌ Failed to deactivate AFK mode: %v", err))
			}
			if !changed {
				return ctx.EditOrReply("ℹ️ <b>AFK Mode is already inactive.</b>")
			}
			return ctx.EditOrReply(fmt.Sprintf("☀️ <b>AFK Mode Deactivated!</b>\n<b>Away for:</b> <code>%s</code>", dur))
		}
		const r = "Away from keyboard"
		if err := p.enableAFK(ctx.Ctx, r); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Failed to activate AFK mode: %v", err))
		}
		return ctx.EditOrReply(fmt.Sprintf("🌙 <b>AFK Mode Activated!</b>\n<b>Reason:</b> <i>%s</i>", html.EscapeString(r)))
	default:
		reason := strings.TrimSpace(ctx.RawArgs)
		if err := p.enableAFK(ctx.Ctx, reason); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Failed to activate AFK mode: %v", err))
		}
		return ctx.EditOrReply(fmt.Sprintf("🌙 <b>AFK Mode Activated!</b>\n<b>Reason:</b> <i>%s</i>", html.EscapeString(reason)))
	}
}

func (p *Plugin) enableAFK(ctx context.Context, reason string) error {
	p.transitionMu.Lock()
	defer p.transitionMu.Unlock()
	now := time.Now().UTC()
	ownerID := p.ownerID
	if p.db != nil && ownerID != 0 {
		if err := p.db.SetAFK(ctx, ownerID, true, reason); err != nil {
			if logger := p.getLogger(); logger != nil {
				logger.Warn("failed to persist AFK status to database", zap.Error(err))
			}
			return fmt.Errorf("persist AFK enable: %w", err)
		}
	}
	p.stateMu.Lock()
	p.state.Store(&afkState{isAFK: true, reason: reason, since: now})
	p.stateMu.Unlock()
	return nil
}

func (p *Plugin) disableAFK(ctx context.Context) (string, bool, error) {
	p.transitionMu.Lock()
	defer p.transitionMu.Unlock()
	st := p.state.Load()
	if st == nil || !st.isAFK {
		return "", false, nil
	}
	dur := formatDuration(time.Since(st.since))
	ownerID := p.ownerID
	if p.db != nil && ownerID != 0 {
		if err := p.db.SetAFK(ctx, ownerID, false, ""); err != nil {
			if logger := p.getLogger(); logger != nil {
				logger.Warn("failed to deactivate AFK status in database", zap.Error(err))
			}
			return "", false, fmt.Errorf("persist AFK disable: %w", err)
		}
	}
	p.stateMu.Lock()
	p.state.Store(&afkState{isAFK: false, reason: "", since: st.since})
	p.stateMu.Unlock()
	return dur, true, nil
}

func (p *Plugin) HandleMessageEvent(ctx context.Context, message *core.MessageEnvelope) error {
	if message == nil {
		return nil
	}
	if decision := core.GetMessageDecision(ctx); decision != nil && (decision.IsSuppressedAFK() || decision.IsSuppressedAutomation()) {
		return nil
	}
	ownerID := p.ownerID
	if ownerID == 0 || p.svcFunc == nil {
		return nil
	}
	svc := p.svcFunc()
	if svc == nil {
		return nil
	}

	if message.Outgoing {
		if svc.IsBotSent(message.ID) {
			return nil
		}
		if decision := core.GetMessageDecision(ctx); decision != nil && decision.Origin() == core.ExecutionAutomation {
			return nil
		}
		if message.IsCommand && strings.EqualFold(message.CommandName, "afk") {
			return nil
		}
		dur, changed, err := p.disableAFK(ctx)
		if err != nil {
			if logger := p.getLogger(); logger != nil {
				logger.Warn("failed to auto-deactivate AFK status", zap.Error(err))
			}
			return nil
		}
		if !changed {
			return nil
		}
		p.Cleanup(0)
		if message.IsPrivate() || !p.isWelcomePrivateOnly() {
			peer := p.resolveEnvelopePeer(ctx, message)
			if peer != nil {
				text := fmt.Sprintf("☀️ <b>Welcome back! AFK mode turned off.</b>\n<b>Away for:</b> <code>%s</code>", dur)
				sent, err := svc.SendMessage(ctx, peer, text)
				if err != nil {
					if logger := p.getLogger(); logger != nil {
						logger.Warn("failed to send welcome back message", zap.Error(err))
					}
				} else if sent != nil && sent.ID > 0 {
					p.deleteWelcomeAfter(svc, peer, sent.ID)
				}
			}
		}
		return nil
	}

	st := p.state.Load()
	if st == nil || !st.isAFK || !p.AutoReplyEnabled() {
		return nil
	}
	if p.getOwnerUsername() == "" && message.Self.Username != "" {
		p.SetOwnerUsername(message.Self.Username)
	}
	senderID := message.Sender.ID
	if senderID == 0 {
		senderID = message.SenderPeer.ID
	}
	if senderID == 0 && message.IsPrivate() {
		senderID = message.ChatID
	}
	if senderID == 0 || senderID == ownerID || message.Sender.IsBot {
		return nil
	}
	chatID := message.ChatID
	if chatID == 0 {
		return nil
	}

	shouldReply := false
	if message.IsPrivate() {
		if message.ChatID == ownerID {
			return nil
		}
		shouldReply = true
	} else if message.IsGroup() || message.IsChannel() {
		shouldReply = message.Mentioned ||
			message.MentionsUser(ownerID) ||
			message.MentionsUsername(p.getOwnerUsername())

		if !shouldReply && message.ReplyToID != 0 && !message.ReplyIsTopicRoot {
			if p.isCooldownActive(chatID, senderID) {
				return nil
			}
			peer := p.resolveEnvelopePeer(ctx, message)
			if peer != nil {
				repliedMsg, err := svc.GetMessage(ctx, peer, message.ReplyToID)
				if err == nil && repliedMsg != nil && (repliedMsg.Out || extractSenderID(repliedMsg) == ownerID) {
					shouldReply = true
				}
			}
		}
	}
	if !shouldReply {
		return nil
	}
	if !p.checkAndSetCooldown(chatID, senderID) {
		return nil
	}
	peer := p.resolveEnvelopePeer(ctx, message)
	if peer == nil {
		p.rollbackCooldown(chatID, senderID)
		return nil
	}
	sinceStr := formatDuration(time.Since(st.since))
	replyText := fmt.Sprintf("🌙 <i>My owner is currently AFK!</i>\n<b>Reason:</b> %s\n<b>Since:</b> <code>%s ago</code>", html.EscapeString(st.reason), sinceStr)
	if _, err := svc.SendMessage(ctx, peer, replyText); err != nil {
		p.rollbackCooldown(chatID, senderID)
		if logger := p.getLogger(); logger != nil {
			logger.Warn("failed to send AFK auto-reply", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int64("sender_id", senderID))
		}
	}
	return nil
}
func (p *Plugin) deleteWelcomeAfter(svc core.TelegramServicer, peer tg.InputPeerClass, messageID int) {
	delay := p.getWelcomeDeleteDelay()
	if delay <= 0 || svc == nil || peer == nil || messageID <= 0 {
		return
	}
	time.AfterFunc(delay, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := svc.DeleteMessage(ctx, peer, []int{messageID}); err != nil {
			if logger := p.getLogger(); logger != nil {
				logger.Warn("failed to auto-delete welcome back message", zap.Int("message_id", messageID), zap.Error(err))
			}
		}
	})
}

func (p *Plugin) isCooldownActive(chatID, senderID int64) bool {
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	last, ok := p.cooldownMap[[2]int64{chatID, senderID}]
	if !ok {
		return false
	}
	dur := p.cooldownDur
	if dur <= 0 {
		return false
	}
	return time.Since(last) < dur
}
func (p *Plugin) checkAndSetCooldown(chatID, senderID int64) bool {
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	key := [2]int64{chatID, senderID}
	now := time.Now()
	dur := p.cooldownDur
	if dur <= 0 {
		return true
	}
	if last, ok := p.cooldownMap[key]; ok && now.Sub(last) < dur {
		return false
	}
	p.cooldownMap[key] = now
	if len(p.cooldownMap) > 1000 {
		cutoff := now.Add(-2 * dur)
		for k, v := range p.cooldownMap {
			if v.Before(cutoff) {
				delete(p.cooldownMap, k)
			}
		}
	}
	return true
}
func (p *Plugin) rollbackCooldown(chatID, senderID int64) {
	p.cooldownMu.Lock()
	delete(p.cooldownMap, [2]int64{chatID, senderID})
	p.cooldownMu.Unlock()
}
func (p *Plugin) resolveEnvelopePeer(ctx context.Context, message *core.MessageEnvelope) tg.InputPeerClass {
	if message == nil {
		return nil
	}
	if peer, err := message.Peer.InputPeer(); err == nil && peer != nil {
		return peer
	}
	resolver := p.getResolver()
	if resolver == nil || message.Peer.ID == 0 {
		return nil
	}
	switch message.Peer.Kind {
	case core.PeerKindUser:
		if peer, _, err := resolver.ResolveUser(ctx, strconv.FormatInt(message.Peer.ID, 10)); err == nil {
			if userPeer, ok := peer.(*tg.InputPeerUser); ok && userPeer.AccessHash != 0 {
				return userPeer
			}
		}
	case core.PeerKindChat:
		return &tg.InputPeerChat{ChatID: message.Peer.ID}
	case core.PeerKindChannel:
		if peer, err := resolver.ResolveChat(ctx, fmt.Sprintf("-100%d", message.Peer.ID)); err == nil {
			if channelPeer, ok := peer.(*tg.InputPeerChannel); ok && channelPeer.AccessHash != 0 {
				return channelPeer
			}
		}
	}
	return nil
}

func extractSenderID(msg *tg.Message) int64 {
	if msg == nil {
		return 0
	}
	if msg.FromID != nil {
		switch p := msg.FromID.(type) {
		case *tg.PeerUser:
			return p.UserID
		case *tg.PeerChannel:
			return p.ChannelID
		case *tg.PeerChat:
			return p.ChatID
		}
	}
	if !msg.Out {
		if u, ok := msg.PeerID.(*tg.PeerUser); ok {
			return u.UserID
		}
	}
	return 0
}
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	mins := d / time.Minute
	d -= mins * time.Minute
	secs := d / time.Second
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	if secs > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", secs))
	}
	return strings.Join(parts, " ")
}
func (p *Plugin) Cleanup(maxAge time.Duration) int {
	if maxAge <= 0 {
		maxAge = 10 * time.Minute
	}
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	now := time.Now()
	purged := 0
	for k, v := range p.cooldownMap {
		if now.Sub(v) > maxAge {
			delete(p.cooldownMap, k)
			purged++
		}
	}
	return purged
}
