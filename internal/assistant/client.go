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
}

var _ Client = (*BotClient)(nil)

func NewBotClient(appID int, appHash string, botToken string, logger *zap.Logger) *BotClient {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BotClient{
		appID:      appID,
		appHash:    appHash,
		botToken:   botToken,
		logger:     logger,
		bridge:     NewBridge(),
		startTime:  time.Now(),
		rateLimits: make(map[int64]*userRateBucket),
	}
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
		msg, ok := update.Message.(*tg.Message)
		if !ok || msg.Out || client == nil {
			return nil
		}
		return c.handleBotCommand(ctx, e, msg, client, adapter)
	})

	// 2. Bot Callback Query Handler (normal message buttons)
	dispatcher.OnBotCallbackQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotCallbackQuery) error {
		chatID := extractChatIDFromPeer(update.Peer)
		inputPeer := callbackInputPeer(update.Peer, e)
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
			_ = c.callbackRouter.Dispatch(ctx, evt, adapter)
		}
		return nil
	})

	// 3. Inline Bot Callback Query Handler (inline message buttons)
	dispatcher.OnInlineBotCallbackQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateInlineBotCallbackQuery) error {
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
			_ = c.callbackRouter.Dispatch(ctx, evt, adapter)
		}
		return nil
	})

	// 4. Bot Inline Query Handler (@bot query)
	dispatcher.OnBotInlineQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotInlineQuery) error {
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
		peer = &tg.InputPeerUser{UserID: senderID, AccessHash: u.AccessHash}
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

func callbackInputPeer(peer tg.PeerClass, e tg.Entities) tg.InputPeerClass {
	if peer == nil {
		return nil
	}
	switch p := peer.(type) {
	case *tg.PeerUser:
		var accessHash int64
		if u, ok := e.Users[p.UserID]; ok && u != nil {
			accessHash = u.AccessHash
		}
		return &tg.InputPeerUser{UserID: p.UserID, AccessHash: accessHash}
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: p.ChatID}
	case *tg.PeerChannel:
		var accessHash int64
		if ch, ok := e.Channels[p.ChannelID]; ok && ch != nil {
			accessHash = ch.AccessHash
		}
		return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: accessHash}
	default:
		return nil
	}
}
