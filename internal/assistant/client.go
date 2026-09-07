package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/localization"
	"github.com/inipew/goultroid/internal/ui/render"
	"go.uber.org/zap"
)

var (
	ErrBotTokenRequired = errors.New("assistant: BOT_TOKEN is required to start assistant bot client")
	ErrAlreadyRunning   = errors.New("assistant: client is already running")
)

type Client interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	IsRunning() bool
	Username() string
	StartTime() time.Time
}

type userRateBucket struct {
	tokens int
	last   time.Time
}

type BotClient struct {
	appID          int
	appHash        string
	botToken       string
	logger         *zap.Logger
	bridge         *Bridge
	callbackRouter *callback.Router
	inlineEngine   *inline.Engine
	eventBus       *core.EventBus
	localizer      localization.Localizer
	cancel         context.CancelFunc
	self           *tg.User
	startTime      time.Time
	mu             sync.RWMutex

	limiterMu  sync.Mutex
	rateLimits map[int64]*userRateBucket

	hashesMu      sync.RWMutex
	userHashes    map[int64]int64
	channelHashes map[int64]int64
}

var _ Client = (*BotClient)(nil)

func NewBotClient(appID int, appHash string, botToken string, logger *zap.Logger) *BotClient {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BotClient{
		appID:         appID,
		appHash:       appHash,
		botToken:      botToken,
		logger:        logger,
		bridge:        NewBridge(),
		startTime:     time.Now(),
		rateLimits:    make(map[int64]*userRateBucket),
		userHashes:    make(map[int64]int64),
		channelHashes: make(map[int64]int64),
	}
}

// CacheEntities stores access hashes for users and channels from received update entities.
func (c *BotClient) CacheEntities(e tg.Entities) {
	c.hashesMu.Lock()
	defer c.hashesMu.Unlock()
	for id, u := range e.Users {
		if u != nil && u.AccessHash != 0 {
			c.userHashes[id] = u.AccessHash
		}
	}
	for id, ch := range e.Channels {
		if ch != nil && ch.AccessHash != 0 {
			c.channelHashes[id] = ch.AccessHash
		}
	}
}

// SetUserAccessHash caches the access hash for a given user.
func (c *BotClient) SetUserAccessHash(userID int64, accessHash int64) {
	if userID == 0 || accessHash == 0 {
		return
	}
	c.hashesMu.Lock()
	defer c.hashesMu.Unlock()
	c.userHashes[userID] = accessHash
}

// GetUserAccessHash retrieves the cached access hash for a given user.
func (c *BotClient) GetUserAccessHash(userID int64) int64 {
	c.hashesMu.RLock()
	defer c.hashesMu.RUnlock()
	return c.userHashes[userID]
}

// SetChannelAccessHash caches the access hash for a given channel.
func (c *BotClient) SetChannelAccessHash(channelID int64, accessHash int64) {
	if channelID == 0 || accessHash == 0 {
		return
	}
	c.hashesMu.Lock()
	defer c.hashesMu.Unlock()
	c.channelHashes[channelID] = accessHash
}

// GetChannelAccessHash retrieves the cached access hash for a given channel.
func (c *BotClient) GetChannelAccessHash(channelID int64) int64 {
	c.hashesMu.RLock()
	defer c.hashesMu.RUnlock()
	return c.channelHashes[channelID]
}

func (c *BotClient) SetBridge(b *Bridge) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bridge = b
}

func (c *BotClient) Bridge() *Bridge {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.bridge
}

func (c *BotClient) SetCallbackRouter(r *callback.Router) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.callbackRouter = r
}

func (c *BotClient) SetInlineEngine(e *inline.Engine) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inlineEngine = e
}

func (c *BotClient) SetEventBus(b *core.EventBus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eventBus = b
}

func (c *BotClient) getEventBus() *core.EventBus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eventBus
}

func (c *BotClient) SetLocalizer(l localization.Localizer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.localizer = l
}

func (c *BotClient) IsRunning() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cancel != nil
}

func (c *BotClient) Username() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.self != nil {
		return c.self.Username
	}
	return ""
}

func (c *BotClient) StartTime() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.startTime.IsZero() {
		return time.Now()
	}
	return c.startTime
}

func (c *BotClient) Start(ctx context.Context) error {
	if c.botToken == "" {
		return ErrBotTokenRequired
	}
	c.mu.Lock()
	if c.cancel != nil {
		c.mu.Unlock()
		return ErrAlreadyRunning
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.startTime = time.Now()
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.cancel = nil
		c.mu.Unlock()
		cancel()
	}()

	var client *telegram.Client
	var adapter *BotServiceAdapter
	dispatcher := tg.NewUpdateDispatcher()

	// 1. Bot Command & Message Handler
	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, update *tg.UpdateNewMessage) error {
		c.CacheEntities(e)
		msg, ok := update.Message.(*tg.Message)
		if !ok || msg.Out || client == nil {
			return nil
		}
		return c.handleBotCommand(ctx, e, msg, client, adapter)
	})

	// 2. Bot Callback Query Handler (normal message buttons)
	dispatcher.OnBotCallbackQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotCallbackQuery) error {
		c.CacheEntities(e)
		chatID := extractChatIDFromPeer(update.Peer)
		inputPeer := c.callbackInputPeer(update.Peer, update.UserID, e)
		target := core.CallbackTarget{
			Origin:       core.CallbackOriginMessage,
			Peer:         inputPeer,
			MessageID:    update.MsgID,
			ChatInstance: update.ChatInstance,
		}
		evt := &core.CallbackQueryEvent{
			At:           time.Now(),
			QueryID:      update.QueryID,
			UserID:       update.UserID,
			ChatID:       chatID,
			MsgID:        update.MsgID,
			Data:         update.Data,
			Origin:       core.CallbackOriginMessage,
			Target:       target,
			ChatInstance: update.ChatInstance,
		}
		if bus := c.getEventBus(); bus != nil {
			bus.Publish(evt)
		}
		if c.callbackRouter != nil && adapter != nil {
			if err := c.callbackRouter.Dispatch(ctx, evt, adapter); err != nil {
				c.logger.Warn("assistant: callback dispatch returned error",
					zap.Error(err),
					zap.Int64("query_id", update.QueryID),
					zap.Int64("user_id", update.UserID),
					zap.ByteString("data", update.Data),
				)
			}
		}
		return nil
	})

	// 3. Inline Bot Callback Query Handler (inline message buttons)
	dispatcher.OnInlineBotCallbackQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateInlineBotCallbackQuery) error {
		c.CacheEntities(e)
		target := core.CallbackTarget{
			Origin:       core.CallbackOriginInline,
			InlineID:     update.MsgID,
			ChatInstance: update.ChatInstance,
		}
		evt := &core.CallbackQueryEvent{
			At:           time.Now(),
			QueryID:      update.QueryID,
			UserID:       update.UserID,
			ChatID:       0,
			MsgID:        0,
			Data:         update.Data,
			Origin:       core.CallbackOriginInline,
			Target:       target,
			ChatInstance: update.ChatInstance,
		}
		if bus := c.getEventBus(); bus != nil {
			bus.Publish(evt)
		}
		if c.callbackRouter != nil && adapter != nil {
			if err := c.callbackRouter.Dispatch(ctx, evt, adapter); err != nil {
				c.logger.Warn("assistant: inline callback dispatch returned error",
					zap.Error(err),
					zap.Int64("query_id", update.QueryID),
					zap.Int64("user_id", update.UserID),
					zap.ByteString("data", update.Data),
				)
			}
		}
		return nil
	})

	// 4. Bot Inline Query Handler (@bot query)
	dispatcher.OnBotInlineQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotInlineQuery) error {
		c.CacheEntities(e)
		if c.inlineEngine == nil || adapter == nil {
			return nil
		}
		return c.inlineEngine.ExecuteWithPeerType(ctx, adapter, update.QueryID, update.UserID, update.Query, update.Offset, update.PeerType)
	})

	// 5. Bot Inline Send Handler (observational feedback)
	dispatcher.OnBotInlineSend(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotInlineSend) error {
		if bus := c.getEventBus(); bus != nil {
			var inlineID tg.InputBotInlineMessageIDClass
			if msgID, ok := update.GetMsgID(); ok {
				inlineID = msgID
			} else if update.MsgID != nil {
				inlineID = update.MsgID
			}
			bus.Publish(&core.InlineResultChosenEvent{
				At:       time.Now(),
				UserID:   update.UserID,
				Query:    update.Query,
				ResultID: update.ID,
				InlineID: inlineID,
			})
		}
		return nil
	})

	gaps := updates.New(updates.Config{Handler: dispatcher})
	client = telegram.NewClient(c.appID, c.appHash, telegram.Options{UpdateHandler: gaps})
	adapter = NewBotServiceAdapter(client.API(), c.logger)

	c.logger.Info("starting assistant bot client...")
	return client.Run(runCtx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("failed to check assistant auth status: %w", err)
		}
		if !status.Authorized {
			if _, err := client.Auth().Bot(ctx, c.botToken); err != nil {
				return fmt.Errorf("failed to authenticate assistant bot token: %w", err)
			}
		}
		self, err := client.Self(ctx)
		if err == nil {
			c.mu.Lock()
			c.self = self
			c.mu.Unlock()
			c.logger.Info("assistant bot authenticated successfully", zap.String("username", self.Username), zap.Int64("id", self.ID))
		}
		if bridge := c.Bridge(); bridge != nil {
			bridge.Dispatch(ctx, Event{Type: EventNotification, Title: "Assistant Started", Message: fmt.Sprintf("Assistant @%s is now online.", c.Username()), CreatedAt: time.Now()})
		}
		<-ctx.Done()
		return ctx.Err()
	})
}

func (c *BotClient) Stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	return nil
}

func (c *BotClient) allowUser(userID int64) bool {
	c.limiterMu.Lock()
	defer c.limiterMu.Unlock()
	if c.rateLimits == nil {
		c.rateLimits = make(map[int64]*userRateBucket)
	}
	now := time.Now()
	b, ok := c.rateLimits[userID]
	if !ok {
		c.rateLimits[userID] = &userRateBucket{tokens: 4, last: now}
		if len(c.rateLimits) > 1000 {
			for id, bucket := range c.rateLimits {
				if now.Sub(bucket.last) > 1*time.Minute {
					delete(c.rateLimits, id)
				}
			}
		}
		return true
	}
	// Refill tokens: 1 token every 2 seconds, max 5 tokens
	elapsed := now.Sub(b.last)
	refill := int(elapsed / (2 * time.Second))
	if refill > 0 {
		b.tokens += refill
		if b.tokens > 5 {
			b.tokens = 5
		}
		b.last = now
	}
	if b.tokens > 0 {
		b.tokens--
		return true
	}
	return false
}

func (c *BotClient) handleBotCommand(ctx context.Context, e tg.Entities, msg *tg.Message, client *telegram.Client, adapter *BotServiceAdapter) error {
	if msg == nil || client == nil {
		return nil
	}
	command := strings.TrimSpace(msg.Message)
	if !strings.HasPrefix(command, "/") {
		return nil
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	cmdName := strings.ToLower(fields[0])
	if atIdx := strings.Index(cmdName, "@"); atIdx != -1 {
		cmdName = cmdName[:atIdx]
	}

	if cmdName != "/start" && cmdName != "/help" && cmdName != "/ping" && cmdName != "/alive" && cmdName != "/status" {
		return nil
	}

	senderID := extractSenderID(msg)
	if senderID == 0 {
		return nil
	}

	if !c.allowUser(senderID) {
		c.logger.Warn("assistant: rate limit exceeded for user", zap.Int64("from_id", senderID), zap.String("command", cmdName))
		return nil
	}

	var peer tg.InputPeerClass
	if u, ok := e.Users[senderID]; ok && u != nil && u.AccessHash != 0 {
		c.SetUserAccessHash(senderID, u.AccessHash)
		peer = &tg.InputPeerUser{UserID: senderID, AccessHash: u.AccessHash}
	} else if hash := c.GetUserAccessHash(senderID); hash != 0 {
		peer = &tg.InputPeerUser{UserID: senderID, AccessHash: hash}
	}
	if peer == nil {
		c.logger.Warn("assistant: sender user access hash missing, command ignored", zap.Int64("sender_id", senderID))
		return nil
	}

	var reply string
	var replyMarkup tg.ReplyMarkupClass

	switch cmdName {
	case "/start":
		screen := RenderStartMenu(c.Username(), c.StartTime())
		reply, replyMarkup = render.ToTelegram(screen)
	case "/help":
		reply = "<b>GoUltroid Assistant</b>\n\n/start — open the interactive dashboard\n/help — show this help\n/ping — check responsiveness\n/status — view system status\n/alive — check assistant status"
	case "/ping":
		reply = "🏓 <b>Pong!</b>"
	case "/status":
		screen := RenderStatusScreen(c.Username(), c.StartTime())
		reply, replyMarkup = render.ToTelegram(screen)
	case "/alive":
		reply = fmt.Sprintf("✅ <b>Alive</b> — assistant @%s is running.", c.Username())
	}

	if adapter != nil {
		_, err := adapter.SendMessageWithMarkup(ctx, peer, reply, replyMarkup)
		if err != nil {
			return fmt.Errorf("assistant send %s reply: %w", cmdName, err)
		}
	} else {
		req := &tg.MessagesSendMessageRequest{
			Peer:     peer,
			Message:  reply,
			RandomID: randomID(),
		}
		if replyMarkup != nil {
			req.SetReplyMarkup(replyMarkup)
		}
		if _, err := client.API().MessagesSendMessage(ctx, req); err != nil {
			return fmt.Errorf("assistant send %s reply: %w", cmdName, err)
		}
	}
	c.logger.Debug("assistant handled command", zap.Int64("from_id", senderID), zap.String("command", cmdName))
	return nil
}

func extractSenderID(msg *tg.Message) int64 {
	if msg == nil {
		return 0
	}
	if msg.FromID != nil {
		if u, ok := msg.FromID.(*tg.PeerUser); ok {
			return u.UserID
		}
	}
	if p, ok := msg.PeerID.(*tg.PeerUser); ok {
		return p.UserID
	}
	return 0
}

func extractChatIDFromPeer(peer tg.PeerClass) int64 {
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

func (c *BotClient) callbackInputPeer(peer tg.PeerClass, userID int64, e tg.Entities) tg.InputPeerClass {
	if peer == nil {
		if userID != 0 {
			if hash := c.GetUserAccessHash(userID); hash != 0 {
				return &tg.InputPeerUser{UserID: userID, AccessHash: hash}
			}
		}
		return nil
	}
	switch p := peer.(type) {
	case *tg.PeerUser:
		var accessHash int64
		if u, ok := e.Users[p.UserID]; ok && u != nil && u.AccessHash != 0 {
			accessHash = u.AccessHash
			c.SetUserAccessHash(p.UserID, accessHash)
		} else {
			accessHash = c.GetUserAccessHash(p.UserID)
		}
		if accessHash == 0 && userID != 0 {
			accessHash = c.GetUserAccessHash(userID)
		}
		return &tg.InputPeerUser{UserID: p.UserID, AccessHash: accessHash}
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: p.ChatID}
	case *tg.PeerChannel:
		var accessHash int64
		if ch, ok := e.Channels[p.ChannelID]; ok && ch != nil && ch.AccessHash != 0 {
			accessHash = ch.AccessHash
			c.SetChannelAccessHash(p.ChannelID, accessHash)
		} else {
			accessHash = c.GetChannelAccessHash(p.ChannelID)
		}
		return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: accessHash}
	default:
		return nil
	}
}
