package assistant

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/localization"
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
	localizer      localization.Localizer
	cancel         context.CancelFunc
	self           *tg.User
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
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.cancel = nil
		c.mu.Unlock()
		cancel()
	}()

	var client *telegram.Client
	dispatcher := tg.NewUpdateDispatcher()
	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, update *tg.UpdateNewMessage) error {
		msg, ok := update.Message.(*tg.Message)
		if !ok || msg.Out || client == nil {
			return nil
		}
		return c.handleBotCommand(ctx, e, msg, client)
	})

	// v1.2 intentionally exposes only the assistant command surface. The
	// userbot inline/callback engines require a Telegram service adapter that
	// the assistant client does not own; registering them with nil service state
	// previously made callbacks/inline handlers unsafe.
	gaps := updates.New(updates.Config{Handler: dispatcher})
	client = telegram.NewClient(c.appID, c.appHash, telegram.Options{UpdateHandler: gaps})
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

func (c *BotClient) handleBotCommand(ctx context.Context, e tg.Entities, msg *tg.Message, client *telegram.Client) error {
	if msg == nil || client == nil {
		return nil
	}
	command := msg.Message
	if command != "/start" && command != "/help" && command != "/ping" && command != "/alive" {
		return nil
	}

	senderID := extractSenderID(msg)
	if senderID == 0 {
		return nil
	}

	if !c.allowUser(senderID) {
		c.logger.Warn("assistant: rate limit exceeded for user", zap.Int64("from_id", senderID), zap.String("command", command))
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
	switch command {
	case "/start":
		reply = "👋 <b>Hello!</b> I am the <b>GoUltroid Assistant Bot</b>.\n\nUse /help to see the available assistant commands."
	case "/help":
		reply = "<b>GoUltroid Assistant</b>\n\n/start — start the assistant\n/help — show this help\n/ping — check responsiveness\n/alive — check assistant status"
	case "/ping":
		reply = "🏓 <b>Pong!</b>"
	case "/alive":
		reply = fmt.Sprintf("✅ <b>Alive</b> — assistant @%s is running.", c.Username())
	}

	_, err := client.API().MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peer, Message: reply})
	if err != nil {
		return fmt.Errorf("assistant send %s reply: %w", command, err)
	}
	c.logger.Debug("assistant handled command", zap.Int64("from_id", senderID), zap.String("command", command))
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
