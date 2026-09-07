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
	"unicode/utf16"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/plugin"
	"go.uber.org/zap"
)

var (
	_ plugin.MessageHookPlugin     = (*Plugin)(nil)
	_ plugin.ContextInitializer    = (*Plugin)(nil)
	_ execution.CapabilityProvider = (*Plugin)(nil)
)

type afkState struct {
	isAFK  bool
	reason string
	since  time.Time
}

// Plugin provides AFK (Away From Keyboard) status and auto-reply capabilities.
type Plugin struct {
	db                 database.Repository
	ownerID            int64
	ownerUsername      string
	svcFunc            func() core.TelegramServicer
	resolver           core.PeerResolver
	logger             *zap.Logger
	welcomePrivateOnly bool

	// In-memory state cache & CAS lock
	stateMu sync.RWMutex
	state   atomic.Pointer[afkState]

	// Compound cooldown map: [2]int64{chatID, senderID} -> time.Time
	cooldownMu  sync.Mutex
	cooldownMap map[[2]int64]time.Time
	cooldownDur time.Duration
}

// New creates a new AFK plugin instance.
func New(db database.Repository, ownerID int64, svcFunc func() core.TelegramServicer) *Plugin {
	p := &Plugin{
		db:                 db,
		ownerID:            ownerID,
		svcFunc:            svcFunc,
		cooldownMap:        make(map[[2]int64]time.Time),
		cooldownDur:        60 * time.Second,
		welcomePrivateOnly: true,
	}
	p.state.Store(&afkState{isAFK: false})
	return p
}

func (p *Plugin) Name() string {
	return "afk"
}

func (p *Plugin) Description() string {
	return "Away From Keyboard status manager and intelligent auto-reply system"
}

// SetOwnerUsername configures the owner's Telegram @username for mention detection.
func (p *Plugin) SetOwnerUsername(username string) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	p.ownerUsername = strings.TrimPrefix(username, "@")
}

func (p *Plugin) getOwnerUsername() string {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()
	return p.ownerUsername
}

// SetResolver configures the PeerResolver for access hash fallback resolution.
func (p *Plugin) SetResolver(resolver core.PeerResolver) {
	p.resolver = resolver
}

// SetLogger configures the structured logger for observability.
func (p *Plugin) SetLogger(logger *zap.Logger) {
	p.logger = logger
}

// SetWelcomePrivateOnly configures whether "Welcome back" is only sent in private chats (default true).
func (p *Plugin) SetWelcomePrivateOnly(privateOnly bool) {
	p.welcomePrivateOnly = privateOnly
}

// SetCooldown configures the auto-reply cooldown duration per chat/sender.
func (p *Plugin) SetCooldown(duration time.Duration) {
	if duration > 0 {
		p.cooldownDur = duration
	}
}

// InitContext initializes the in-memory AFK state from database on startup.
func (p *Plugin) InitContext(ctx context.Context) error {
	return p.loadState(ctx)
}

// Init initializes the plugin (legacy fallback).
func (p *Plugin) Init() error {
	return p.loadState(context.Background())
}

func (p *Plugin) loadState(ctx context.Context) error {
	if p.db == nil || p.ownerID == 0 {
		return nil
	}
	status, err := p.db.GetAFK(ctx, p.ownerID)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("failed to load initial AFK status from database", zap.Error(err))
		}
		return nil
	}
	if status != nil && status.IsAFK {
		p.state.Store(&afkState{
			isAFK:  true,
			reason: status.Reason,
			since:  status.Since,
		})
	} else {
		p.state.Store(&afkState{isAFK: false})
	}
	return nil
}

// MessageHookPriority returns priority for the message hook (Feature = 50).
func (p *Plugin) MessageHookPriority() int {
	return 50
}

// Capabilities declares the capabilities provided by this plugin (§4, §28 bug16_1).
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{
		{
			ID:          "afk",
			Name:        "AFK",
			Description: "Away From Keyboard status manager and intelligent auto-reply system",
			Category:    "Utility",
			Surfaces:    execution.SurfaceUserbot,
		},
	}
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "afk",
			Description: "Set AFK status with an optional reason, toggle, or off",
			Usage:       ".afk [on|off|status|toggle|reason]",
			Category:    "AFK",
			Permission:  core.PermissionOwner,
			Surfaces:    execution.SurfaceUserbot,
			Handler:     p.handleAFKCommand,
		},
	}
}

func (p *Plugin) handleAFKCommand(ctx *core.Context) error {
	ownerID := p.ownerID
	if ownerID == 0 {
		ownerID = ctx.SenderID()
	}

	if len(ctx.Args) == 0 {
		// Toggle semantics
		st := p.state.Load()
		if st != nil && st.isAFK {
			dur, _ := p.disableAFK(ctx.Ctx)
			return ctx.EditOrReply(fmt.Sprintf("☀️ <b>AFK Mode Deactivated!</b>\n<b>Away for:</b> <code>%s</code>", dur))
		}
		const defaultReason = "Away from keyboard"
		p.enableAFK(ctx.Ctx, defaultReason)
		return ctx.EditOrReply(fmt.Sprintf("🌙 <b>AFK Mode Activated!</b>\n<b>Reason:</b> <i>%s</i>", html.EscapeString(defaultReason)))
	}

	sub := strings.ToLower(ctx.Args[0])
	switch sub {
	case "off", "disable", "stop":
		dur, changed := p.disableAFK(ctx.Ctx)
		if !changed {
			return ctx.EditOrReply("ℹ️ <b>AFK Mode is already inactive.</b>")
		}
		return ctx.EditOrReply(fmt.Sprintf("☀️ <b>AFK Mode Deactivated!</b>\n<b>Away for:</b> <code>%s</code>", dur))
	case "status":
		st := p.state.Load()
		if st != nil && st.isAFK {
			sinceStr := formatDuration(time.Since(st.since))
			return ctx.EditOrReply(fmt.Sprintf("🌙 <b>AFK Status: Active</b>\n<b>Reason:</b> <i>%s</i>\n<b>Since:</b> <code>%s ago</code>",
				html.EscapeString(st.reason), sinceStr))
		}
		return ctx.EditOrReply("🟢 <b>AFK Status: Inactive</b>")
	case "on":
		reason := "Away from keyboard"
		if len(ctx.Args) > 1 {
			reason = strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, ctx.Args[0]))
		}
		p.enableAFK(ctx.Ctx, reason)
		return ctx.EditOrReply(fmt.Sprintf("🌙 <b>AFK Mode Activated!</b>\n<b>Reason:</b> <i>%s</i>", html.EscapeString(reason)))
	case "toggle":
		st := p.state.Load()
		if st != nil && st.isAFK {
			dur, _ := p.disableAFK(ctx.Ctx)
			return ctx.EditOrReply(fmt.Sprintf("☀️ <b>AFK Mode Deactivated!</b>\n<b>Away for:</b> <code>%s</code>", dur))
		}
		const defaultReason = "Away from keyboard"
		p.enableAFK(ctx.Ctx, defaultReason)
		return ctx.EditOrReply(fmt.Sprintf("🌙 <b>AFK Mode Activated!</b>\n<b>Reason:</b> <i>%s</i>", html.EscapeString(defaultReason)))
	default:
		// Entire RawArgs is the custom reason
		reason := strings.TrimSpace(ctx.RawArgs)
		p.enableAFK(ctx.Ctx, reason)
		return ctx.EditOrReply(fmt.Sprintf("🌙 <b>AFK Mode Activated!</b>\n<b>Reason:</b> <i>%s</i>", html.EscapeString(reason)))
	}
}

func (p *Plugin) enableAFK(ctx context.Context, reason string) {
	now := time.Now().UTC()
	p.stateMu.Lock()
	p.state.Store(&afkState{
		isAFK:  true,
		reason: reason,
		since:  now,
	})
	p.stateMu.Unlock()

	if p.db != nil {
		ownerID := p.ownerID
		if ownerID != 0 {
			if err := p.db.SetAFK(ctx, ownerID, true, reason); err != nil && p.logger != nil {
				p.logger.Warn("failed to persist AFK status to database", zap.Error(err))
			}
		}
	}
}

func (p *Plugin) disableAFK(ctx context.Context) (string, bool) {
	p.stateMu.Lock()
	st := p.state.Load()
	if st == nil || !st.isAFK {
		p.stateMu.Unlock()
		return "", false
	}
	dur := formatDuration(time.Since(st.since))
	p.state.Store(&afkState{
		isAFK:  false,
		reason: "",
		since:  st.since,
	})
	p.stateMu.Unlock()

	if p.db != nil {
		ownerID := p.ownerID
		if ownerID != 0 {
			if err := p.db.SetAFK(ctx, ownerID, false, ""); err != nil && p.logger != nil {
				p.logger.Warn("failed to deactivate AFK status in database", zap.Error(err))
			}
		}
	}
	return dur, true
}

// HandleIncomingMessage intercepts all incoming messages to handle auto-reply and auto-unafk.
func (p *Plugin) HandleIncomingMessage(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error {
	if msg == nil {
		return nil
	}

	// Check arbitration suppression from PMPermit, Blacklist, or Filters
	if decision := core.GetMessageDecision(ctx); decision != nil {
		if decision.IsSuppressedAFK() || decision.IsSuppressedAutomation() {
			return nil
		}
	}

	ownerID := p.ownerID
	if ownerID == 0 {
		return nil
	}

	svc := p.svcFunc()
	if svc == nil {
		return nil
	}

	// 1. Message from Owner (outgoing) -> Auto-deactivate AFK
	if msg.Out {
		// NEVER auto-unAFK if this outgoing message was dispatched programmatically by GoUltroid
		// (e.g. Scheduler, Broadcast, Addon, AI, Downloader, AFK reply, PMPermit warning)
		if svc.IsBotSent(msg.ID) {
			return nil
		}
		if decision := core.GetMessageDecision(ctx); decision != nil && decision.Origin() == core.ExecutionAutomation {
			return nil
		}
		if isCommand && strings.EqualFold(cmdName, "afk") {
			return nil // Don't deactivate if running .afk
		}

		dur, changed := p.disableAFK(ctx)
		if !changed {
			return nil // Wasn't AFK or already transitioned by concurrent message
		}
		p.Cleanup(0)

		// Welcome back announcement: only send in private chat if welcomePrivateOnly is true!
		// Silent in groups to prevent bot spam in public groups.
		isPrivate := false
		if _, ok := msg.PeerID.(*tg.PeerUser); ok {
			isPrivate = true
		}
		if isPrivate || !p.welcomePrivateOnly {
			peer := p.resolveInputPeer(ctx, msg.PeerID, e)
			if peer != nil {
				text := fmt.Sprintf("☀️ <b>Welcome back! AFK mode turned off.</b>\n<b>Away for:</b> <code>%s</code>", dur)
				if _, err := svc.SendMessage(ctx, peer, text); err != nil && p.logger != nil {
					p.logger.Warn("failed to send welcome back message", zap.Error(err))
				}
			}
		}
		return nil
	}

	// 2. Incoming message -> Fast in-memory check (0 DB queries on hot path)
	st := p.state.Load()
	if st == nil || !st.isAFK {
		return nil
	}

	// Auto-discover owner's username if not configured yet and present in update entities
	if p.getOwnerUsername() == "" {
		if u, ok := e.Users[ownerID]; ok && u != nil && u.Username != "" {
			p.SetOwnerUsername(u.Username)
		}
	}

	senderID := extractSenderID(msg)
	if senderID == 0 || senderID == ownerID {
		return nil // Ignore unknown sender or self
	}

	// Prevent bot loop: ignore messages sent by bot users
	if u, ok := e.Users[senderID]; ok && u != nil && u.Bot {
		return nil
	}

	chatID := extractChatID(msg.PeerID)
	if chatID == 0 {
		return nil
	}

	shouldReply := false
	switch pPeer := msg.PeerID.(type) {
	case *tg.PeerUser:
		// Private chat (DM)
		if pPeer.UserID == ownerID {
			return nil // Saved messages
		}
		shouldReply = true

	case *tg.PeerChat, *tg.PeerChannel:
		// Group / Supergroup / Channel
		// Check mention (msg.Mentioned, MessageEntityMentionName, MessageEntityMention)
		if msg.Mentioned {
			shouldReply = true
		}
		if !shouldReply && len(msg.Entities) > 0 {
			u16 := utf16.Encode([]rune(msg.Message))
			ownerUsername := p.getOwnerUsername()
			for _, ent := range msg.Entities {
				switch m := ent.(type) {
				case *tg.MessageEntityMentionName:
					if m.UserID == ownerID {
						shouldReply = true
						break
					}
				case *tg.MessageEntityMention:
					if ownerUsername != "" && m.Offset >= 0 && m.Offset+m.Length <= len(u16) {
						mentionStr := string(utf16.Decode(u16[m.Offset : m.Offset+m.Length]))
						mText := strings.TrimPrefix(strings.ToLower(mentionStr), "@")
						if mText == strings.ToLower(ownerUsername) {
							shouldReply = true
							break
						}
					}
				}
				if shouldReply {
					break
				}
			}
		}

		// Check reply-to-owner
		if !shouldReply && msg.ReplyTo != nil {
			if h, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok && h.ReplyToMsgID != 0 {
				// Avoid false positives in Telegram Forum Topics:
				// When someone posts in a forum topic without replying to anyone, Telegram sets
				// ForumTopic: true and ReplyToMsgID points to the topic root creation message.
				// If the owner created the topic, EVERY post in that topic would falsely trigger AFK!
				isTopicRootPost := h.ForumTopic && (h.ReplyToTopID == 0 || h.ReplyToMsgID == h.ReplyToTopID)
				if !isTopicRootPost {
					// IMPORTANT: Check cooldown FIRST before expensive Telegram MTProto RPC GetMessage!
					if p.isCooldownActive(chatID, senderID) {
						return nil
					}
					peer := p.resolveInputPeer(ctx, msg.PeerID, e)
					if peer != nil {
						repliedMsg, err := svc.GetMessage(ctx, peer, h.ReplyToMsgID)
						if err == nil && repliedMsg != nil {
							if repliedMsg.Out || extractSenderID(repliedMsg) == ownerID {
								shouldReply = true
							}
						}
					}
				}
			}
		}
	}

	if !shouldReply {
		return nil
	}

	// 3. Thread-safe atomic check-and-set compound cooldown per (chatID, senderID)
	if !p.checkAndSetCooldown(chatID, senderID) {
		return nil
	}

	peer := p.resolveInputPeer(ctx, msg.PeerID, e)
	if peer == nil {
		return nil
	}

	sinceStr := formatDuration(time.Since(st.since))
	replyText := fmt.Sprintf("🌙 <i>My owner is currently AFK!</i>\n<b>Reason:</b> %s\n<b>Since:</b> <code>%s ago</code>",
		html.EscapeString(st.reason), sinceStr)

	if _, err := svc.SendMessage(ctx, peer, replyText); err != nil && p.logger != nil {
		p.logger.Warn("failed to send AFK auto-reply",
			zap.Error(err),
			zap.Int64("chat_id", chatID),
			zap.Int64("sender_id", senderID),
		)
	}
	return nil
}

func (p *Plugin) isCooldownActive(chatID, senderID int64) bool {
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	key := [2]int64{chatID, senderID}
	last, exists := p.cooldownMap[key]
	if !exists {
		return false
	}
	dur := p.cooldownDur
	if dur <= 0 {
		dur = 60 * time.Second
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
		dur = 60 * time.Second
	}
	if last, exists := p.cooldownMap[key]; exists && now.Sub(last) < dur {
		return false
	}
	p.cooldownMap[key] = now

	// Auto-prune if map grows large to prevent memory leaks
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

func (p *Plugin) resolveInputPeer(ctx context.Context, peer tg.PeerClass, e tg.Entities) tg.InputPeerClass {
	res := extractPeerInput(peer, e)
	if res != nil {
		return res
	}
	if p.resolver == nil {
		return nil
	}
	switch pt := peer.(type) {
	case *tg.PeerUser:
		if ipu, _, err := p.resolver.ResolveUser(ctx, strconv.FormatInt(pt.UserID, 10)); err == nil && ipu != nil {
			return ipu
		}
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: pt.ChatID}
	case *tg.PeerChannel:
		if ipc, err := p.resolver.ResolveChat(ctx, fmt.Sprintf("-100%d", pt.ChannelID)); err == nil && ipc != nil {
			return ipc
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
	// In incoming 1-on-1 private messages (DM), Telegram MTProto updates frequently omit msg.FromID
	// because msg.PeerID already uniquely identifies the sender.
	if !msg.Out {
		if u, ok := msg.PeerID.(*tg.PeerUser); ok {
			return u.UserID
		}
	}
	return 0
}

func extractChatID(peer tg.PeerClass) int64 {
	if peer == nil {
		return 0
	}
	switch p := peer.(type) {
	case *tg.PeerUser:
		return p.UserID
	case *tg.PeerChat:
		return p.ChatID
	case *tg.PeerChannel:
		return p.ChannelID
	}
	return 0
}

func extractPeerInput(peer tg.PeerClass, e tg.Entities) tg.InputPeerClass {
	if peer == nil {
		return nil
	}
	switch p := peer.(type) {
	case *tg.PeerUser:
		if p.UserID == 0 {
			return nil
		}
		if u, ok := e.Users[p.UserID]; ok && u != nil && u.AccessHash != 0 {
			return &tg.InputPeerUser{UserID: p.UserID, AccessHash: u.AccessHash}
		}
		return nil
	case *tg.PeerChat:
		if p.ChatID == 0 {
			return nil
		}
		return &tg.InputPeerChat{ChatID: p.ChatID}
	case *tg.PeerChannel:
		if p.ChannelID == 0 {
			return nil
		}
		if ch, ok := e.Channels[p.ChannelID]; ok && ch != nil && ch.AccessHash != 0 {
			return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: ch.AccessHash}
		}
		return nil
	}
	return nil
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

// Cleanup purges cooldown records older than maxAge to prevent memory accumulation.
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
