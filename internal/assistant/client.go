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
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/localization"
	"go.uber.org/zap"
)

var (
	ErrBotTokenRequired = errors.New("assistant: BOT_TOKEN is required to start assistant bot client")
	ErrAlreadyRunning = errors.New("assistant: client is already running")
)

type Client interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	IsRunning() bool
	Username() string
}

type BotClient struct {
	appID int
	appHash string
	botToken string
	logger *zap.Logger
	bridge *Bridge
	callbackRouter *callback.Router
	inlineEngine *inline.Engine
	localizer localization.Localizer
	cancel context.CancelFunc
	self *tg.User
	mu sync.RWMutex
}

var _ Client = (*BotClient)(nil)

func NewBotClient(appID int, appHash string, botToken string, logger *zap.Logger) *BotClient {
	if logger == nil { logger = zap.NewNop() }
	return &BotClient{appID: appID, appHash: appHash, botToken: botToken, logger: logger, bridge: NewBridge()}
}
func (c *BotClient) SetBridge(b *Bridge) { c.mu.Lock(); defer c.mu.Unlock(); c.bridge = b }
func (c *BotClient) Bridge() *Bridge { c.mu.RLock(); defer c.mu.RUnlock(); return c.bridge }
func (c *BotClient) SetCallbackRouter(r *callback.Router) { c.mu.Lock(); defer c.mu.Unlock(); c.callbackRouter = r }
func (c *BotClient) SetInlineEngine(e *inline.Engine) { c.mu.Lock(); defer c.mu.Unlock(); c.inlineEngine = e }
func (c *BotClient) SetLocalizer(l localization.Localizer) { c.mu.Lock(); defer c.mu.Unlock(); c.localizer = l }
func (c *BotClient) IsRunning() bool { c.mu.RLock(); defer c.mu.RUnlock(); return c.cancel != nil }
func (c *BotClient) Username() string { c.mu.RLock(); defer c.mu.RUnlock(); if c.self != nil { return c.self.Username }; return "" }

func (c *BotClient) Start(ctx context.Context) error {
	if c.botToken == "" { return ErrBotTokenRequired }
	c.mu.Lock()
	if c.cancel != nil { c.mu.Unlock(); return ErrAlreadyRunning }
	runCtx, cancel := context.WithCancel(ctx); c.cancel = cancel; c.mu.Unlock()
	defer func() { c.mu.Lock(); c.cancel = nil; c.mu.Unlock(); cancel() }()

	dispatcher := tg.NewUpdateDispatcher()
	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, update *tg.UpdateNewMessage) error {
		msg, ok := update.Message.(*tg.Message)
		if !ok || msg.Out { return nil }
		return c.handleBotCommand(ctx, e, msg, client)
	})
	dispatcher.OnBotInlineQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotInlineQuery) error {
		c.mu.RLock(); engine := c.inlineEngine; c.mu.RUnlock()
		if engine != nil { return engine.Execute(ctx, nil, update.QueryID, update.UserID, update.Query, update.Offset) }
		return nil
	})
	dispatcher.OnBotCallbackQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotCallbackQuery) error {
		c.mu.RLock(); cbRouter := c.callbackRouter; c.mu.RUnlock()
		if cbRouter != nil {
			evt := &core.CallbackQueryEvent{At: time.Now(), QueryID: update.QueryID, UserID: update.UserID, MsgID: update.MsgID, Data: update.Data}
			return cbRouter.Dispatch(ctx, evt, nil)
		}
		return nil
	})

	gaps := updates.New(updates.Config{Handler: dispatcher})
	client := telegram.NewClient(c.appID, c.appHash, telegram.Options{UpdateHandler: gaps})
	c.logger.Info("starting assistant bot client...")
	return client.Run(runCtx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil { return fmt.Errorf("failed to check assistant auth status: %w", err) }
		if !status.Authorized {
			if _, err := client.Auth().Bot(ctx, c.botToken); err != nil { return fmt.Errorf("failed to authenticate assistant bot token: %w", err) }
		}
		self, err := client.Self(ctx)
		if err == nil {
			c.mu.Lock(); c.self = self; c.mu.Unlock()
			c.logger.Info("assistant bot authenticated successfully", zap.String("username", self.Username), zap.Int64("id", self.ID))
		}
		c.Bridge().Dispatch(ctx, Event{Type: EventNotification, Title: "Assistant Started", Message: fmt.Sprintf("Assistant @%s is now online.", c.Username()), CreatedAt: time.Now()})
		<-ctx.Done()
		return ctx.Err()
	})
}

func (c *BotClient) Stop(ctx context.Context) error {
	c.mu.Lock(); defer c.mu.Unlock()
	if c.cancel != nil { c.cancel(); c.cancel = nil }
	return nil
}

// handleBotCommand implements the assistant's minimal command surface and sends
// an actual Telegram response instead of only constructing an unused string.
func (c *BotClient) handleBotCommand(ctx context.Context, e tg.Entities, msg *tg.Message, client *telegram.Client) error {
	if msg == nil { return nil }
	if msg.Message != "/start" && msg.Message != "/help" { return nil }
	senderID := extractSenderID(msg)
	if senderID == 0 { return nil }

	peer := tg.InputPeerClass(&tg.InputPeerUser{UserID: senderID})
	if u, ok := e.Users[senderID]; ok { peer = &tg.InputPeerUser{UserID: senderID, AccessHash: u.AccessHash} }
	reply := "👋 <b>Hello!</b> I am the <b>GoUltroid Assistant Bot</b>.\n\n• <b>Core:</b> GoUltroid Parity Engine\n• <b>Status:</b> Active\n\nUse inline queries or buttons to interact."
	_, err := client.API().MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peer, Message: reply})
	if err != nil { return fmt.Errorf("assistant send %s reply: %w", msg.Message, err) }
	c.logger.Debug("assistant handled command", zap.Int64("from_id", senderID), zap.String("command", msg.Message))
	return nil
}

func extractSenderID(msg *tg.Message) int64 {
	if msg == nil { return 0 }
	if msg.FromID != nil { if u, ok := msg.FromID.(*tg.PeerUser); ok { return u.UserID } }
	if p, ok := msg.PeerID.(*tg.PeerUser); ok { return p.UserID }
	return 0
}
