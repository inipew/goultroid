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
	asstcb "github.com/inipew/goultroid/internal/assistant/callback"
	asstclient "github.com/inipew/goultroid/internal/assistant/client"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/assistant/presentation"
	"github.com/inipew/goultroid/internal/core"
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
	StartTime() time.Time
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

	// Assistant v2 Subsystems
	lifecycle   *asstclient.Lifecycle
	rateLimiter asstclient.RateLimiter
	resolver    peer.Resolver
	v2Router    *asstcb.Router
	cmdRouter   *command.Router
	menuCtrl    *menu.Controller
	interaction *interaction.ClientInteraction
}

var _ Client = (*BotClient)(nil)

func NewBotClient(appID int, appHash string, botToken string, logger *zap.Logger) *BotClient {
	if logger == nil {
		logger = zap.NewNop()
	}

	cache := peer.NewMemoryCache()
	res := peer.NewResolver(cache)
	rl := asstclient.NewUserRateLimiter(5, 2*time.Second)
	v2r := asstcb.NewRouter(logger)
	cmdR := command.NewRouter(logger)
	ctrl := menu.NewController(presentation.RenderScreen)

	client := &BotClient{
		appID:       appID,
		appHash:     appHash,
		botToken:    botToken,
		logger:      logger,
		bridge:      NewBridge(),
		startTime:   time.Now(),
		lifecycle:   asstclient.NewLifecycle(),
		rateLimiter: rl,
		resolver:    res,
		v2Router:    v2r,
		cmdRouter:   cmdR,
		menuCtrl:    ctrl,
	}

	ctrl.AttachRoutes(v2r, client.Username, client.StartTime)
	command.AttachDefaultCommands(cmdR, client.Username, client.StartTime, presentation.RenderScreen)

	return client
}

// CacheEntities stores access hashes for users and channels from received update entities.
func (c *BotClient) CacheEntities(e tg.Entities) {
	if c.resolver != nil && c.resolver.Cache() != nil {
		c.resolver.Cache().CacheEntities(e)
	}
}

// SetUserAccessHash caches the access hash for a given user.
func (c *BotClient) SetUserAccessHash(userID int64, accessHash int64) {
	if userID == 0 || accessHash == 0 {
		return
	}
	if c.resolver != nil && c.resolver.Cache() != nil {
		c.resolver.Cache().Put(peer.PeerRecord{
			ID:         userID,
			Kind:       peer.PeerKindUser,
			AccessHash: accessHash,
			UpdatedAt:  time.Now(),
		})
	}
}

// GetUserAccessHash retrieves the cached access hash for a given user.
func (c *BotClient) GetUserAccessHash(userID int64) int64 {
	if c.resolver != nil && c.resolver.Cache() != nil {
		if rec, ok := c.resolver.Cache().Get(peer.PeerKindUser, userID); ok {
			return rec.AccessHash
		}
	}
	return 0
}

// SetChannelAccessHash caches the access hash for a given channel.
func (c *BotClient) SetChannelAccessHash(channelID int64, accessHash int64) {
	if channelID == 0 || accessHash == 0 {
		return
	}
	if c.resolver != nil && c.resolver.Cache() != nil {
		c.resolver.Cache().Put(peer.PeerRecord{
			ID:         channelID,
			Kind:       peer.PeerKindChannel,
			AccessHash: accessHash,
			UpdatedAt:  time.Now(),
		})
	}
}

// GetChannelAccessHash retrieves the cached access hash for a given channel.
func (c *BotClient) GetChannelAccessHash(channelID int64) int64 {
	if c.resolver != nil && c.resolver.Cache() != nil {
		if rec, ok := c.resolver.Cache().Get(peer.PeerKindChannel, channelID); ok {
			return rec.AccessHash
		}
	}
	return 0
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
	return c.lifecycle.State() == asstclient.StateRunning
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

// V2Router returns the Assistant v2 callback Router.
func (c *BotClient) V2Router() *asstcb.Router {
	return c.v2Router
}

// CommandRouter returns the Assistant v2 command Router.
func (c *BotClient) CommandRouter() *command.Router {
	return c.cmdRouter
}

// PeerResolver returns the peer Resolver.
func (c *BotClient) PeerResolver() peer.Resolver {
	return c.resolver
}

// Start connects and runs the bot MTProto client loop.
func (c *BotClient) Start(ctx context.Context) error {
	if c.botToken == "" {
		return ErrBotTokenRequired
	}
	if !c.lifecycle.TryStart() {
		return ErrAlreadyRunning
	}

	c.mu.Lock()
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.startTime = time.Now()
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.cancel = nil
		c.mu.Unlock()
		cancel()
		c.lifecycle.SetState(asstclient.StateStopped)
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

		senderID := extractSenderID(msg)
		if senderID == 0 {
			return nil
		}

		if !c.rateLimiter.Allow(senderID, "command") {
			c.logger.Warn("assistant: rate limit exceeded for user", zap.Int64("from_id", senderID))
			return nil
		}

		inputPeer, err := c.resolver.Resolve(ctx, msg.PeerID, senderID, e)
		if err != nil || inputPeer == nil {
			c.logger.Warn("assistant: sender access hash missing, command ignored", zap.Int64("sender_id", senderID), zap.Error(err))
			return nil
		}

		if c.interaction != nil {
			err := c.cmdRouter.Dispatch(ctx, senderID, inputPeer, msg.Message, c.interaction)
			if err != nil && !errors.Is(err, command.ErrUnknownCommand) {
				c.logger.Warn("assistant: command error", zap.Error(err), zap.Int64("sender_id", senderID))
			}
		}
		return nil
	})

	// 2. Bot Callback Query Handler (normal message buttons)
	dispatcher.OnBotCallbackQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotCallbackQuery) error {
		c.CacheEntities(e)
		chatID := extractChatIDFromPeer(update.Peer)
		inputPeer, _ := c.resolver.Resolve(ctx, update.Peer, update.UserID, e)
		target := interaction.NewMessageTarget(inputPeer, update.MsgID, chatID, update.ChatInstance)

		// Check rate limit
		if !c.rateLimiter.Allow(update.UserID, "callback") {
			if c.interaction != nil {
				_ = c.interaction.Answer(ctx, update.QueryID, "Too many requests. Please wait.", true)
			}
			return nil
		}

		// Try parsing Assistant v2 callback payload
		payload, parseErr := asstcb.Parse(update.Data)
		if parseErr == nil && payload.Namespace == "assistant" && c.interaction != nil {
			tx := asstcb.NewTransaction(update.QueryID, update.UserID, *payload, target, c.interaction)
			if err := c.v2Router.Dispatch(ctx, tx); err != nil {
				c.logger.Warn("assistant: v2 callback error",
					zap.Error(err),
					zap.Int64("query_id", update.QueryID),
					zap.Int64("user_id", update.UserID),
					zap.String("action", payload.Action),
				)
			}
			return nil
		}

		// Publish event to event bus
		evt := &core.CallbackQueryEvent{
			At:           time.Now(),
			QueryID:      update.QueryID,
			UserID:       update.UserID,
			ChatID:       chatID,
			MsgID:        update.MsgID,
			Data:         update.Data,
			Origin:       core.CallbackOriginMessage,
			Target: core.CallbackTarget{
				Origin:       core.CallbackOriginMessage,
				Peer:         inputPeer,
				MessageID:    update.MsgID,
				ChatInstance: update.ChatInstance,
			},
			ChatInstance: update.ChatInstance,
		}
		if bus := c.getEventBus(); bus != nil {
			bus.Publish(evt)
		}

		// Fallback to legacy callback router (e.g. for settings / help plugins)
		if c.callbackRouter != nil && adapter != nil {
			if err := c.callbackRouter.Dispatch(ctx, evt, adapter); err != nil {
				c.logger.Warn("assistant: legacy callback dispatch returned error",
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
	c.interaction = interaction.NewClientInteraction(client.API(), c.logger)

	c.logger.Info("starting assistant bot client (v2)...")
	c.lifecycle.SetState(asstclient.StateRunning)

	return client.Run(runCtx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			c.lifecycle.SetState(asstclient.StateFailed)
			return fmt.Errorf("failed to check assistant auth status: %w", err)
		}
		if !status.Authorized {
			if _, err := client.Auth().Bot(ctx, c.botToken); err != nil {
				c.lifecycle.SetState(asstclient.StateFailed)
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

// Stop terminates the running assistant bot client.
func (c *BotClient) Stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.lifecycle.SetState(asstclient.StateStopped)
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
