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
	// ErrBotTokenRequired indicates that the assistant bot client cannot start without a bot token.
	ErrBotTokenRequired = errors.New("assistant: BOT_TOKEN is required to start assistant bot client")
	// ErrAlreadyRunning indicates that the assistant bot is already started.
	ErrAlreadyRunning = errors.New("assistant: client is already running")
)

// Client defines the lifecycle management interface for the assistant bot.
type Client interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	IsRunning() bool
	Username() string
}

// BotClient coordinates the dedicated Telegram Bot Client lifecycle and message routing.
type BotClient struct {
	appID          int
	appHash        string
	botToken       string
	logger         *zap.Logger
	bridge         *Bridge
	callbackRouter *callback.Router
	inlineEngine   *inline.Engine
	localizer      localization.Localizer

	cancel context.CancelFunc
	self   *tg.User
	mu     sync.RWMutex
}

// Ensure BotClient implements Client.
var _ Client = (*BotClient)(nil)

// NewBotClient creates a new assistant BotClient instance.
func NewBotClient(appID int, appHash string, botToken string, logger *zap.Logger) *BotClient {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BotClient{
		appID:    appID,
		appHash:  appHash,
		botToken: botToken,
		logger:   logger,
		bridge:   NewBridge(),
	}
}

// SetBridge updates the userbot <-> assistant bridge.
func (c *BotClient) SetBridge(b *Bridge) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bridge = b
}

// Bridge returns the active bridge.
func (c *BotClient) Bridge() *Bridge {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.bridge
}

// SetCallbackRouter sets the shared callback router.
func (c *BotClient) SetCallbackRouter(r *callback.Router) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.callbackRouter = r
}

// SetInlineEngine sets the shared inline engine.
func (c *BotClient) SetInlineEngine(e *inline.Engine) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inlineEngine = e
}

// SetLocalizer sets the localization service.
func (c *BotClient) SetLocalizer(l localization.Localizer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.localizer = l
}

// IsRunning returns true if the assistant bot client is actively running.
func (c *BotClient) IsRunning() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cancel != nil
}

// Username returns the assistant bot username (e.g. "MyUltroidAssistantBot").
func (c *BotClient) Username() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.self != nil {
		return c.self.Username
	}
	return ""
}

// Start connects the assistant bot to Telegram MTProto and logs in with Bot Token.
// It blocks until ctx is canceled or Stop() is called.
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

	dispatcher := tg.NewUpdateDispatcher()

	// Handle /start and private commands for assistant
	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, update *tg.UpdateNewMessage) error {
		msg, ok := update.Message.(*tg.Message)
		if !ok || msg.Out {
			return nil
		}
		c.handleBotCommand(ctx, e, msg)
		return nil
	})

	// Route inline queries to shared inline engine
	dispatcher.OnBotInlineQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotInlineQuery) error {
		c.mu.RLock()
		engine := c.inlineEngine
		c.mu.RUnlock()
		if engine != nil {
			return engine.Execute(ctx, nil, update.QueryID, update.UserID, update.Query, update.Offset)
		}
		return nil
	})

	// Route callback queries to shared callback router
	dispatcher.OnBotCallbackQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotCallbackQuery) error {
		c.mu.RLock()
		cbRouter := c.callbackRouter
		c.mu.RUnlock()
		if cbRouter != nil {
			evt := &core.CallbackQueryEvent{
				At:      time.Now(),
				QueryID: update.QueryID,
				UserID:  update.UserID,
				MsgID:   update.MsgID,
				Data:    update.Data,
			}
			return cbRouter.Dispatch(ctx, evt, nil)
		}
		return nil
	})

	gaps := updates.New(updates.Config{
		Handler: dispatcher,
	})

	client := telegram.NewClient(c.appID, c.appHash, telegram.Options{
		UpdateHandler: gaps,
	})

	c.logger.Info("starting assistant bot client...")

	return client.Run(runCtx, func(ctx context.Context) error {
		// Authenticate bot token
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
			c.logger.Info("assistant bot authenticated successfully",
				zap.String("username", self.Username),
				zap.Int64("id", self.ID),
			)
		}

		// Dispatch bot started event to bridge
		c.Bridge().Dispatch(ctx, Event{
			Type:      EventNotification,
			Title:     "Assistant Started",
			Message:   fmt.Sprintf("Assistant @%s is now online.", c.Username()),
			CreatedAt: time.Now(),
		})

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
	return nil
}

func (c *BotClient) handleBotCommand(ctx context.Context, e tg.Entities, msg *tg.Message) {
	text := msg.Message
	if text == "/start" || text == "/help" {
		reply := fmt.Sprintf("👋 <b>Hello!</b> I am the <b>GoUltroid Assistant Bot</b>.\n\n• <b>Core:</b> GoUltroid Parity Engine\n• <b>Status:</b> Active\n\nUse inline queries or buttons to interact.")
		c.logger.Debug("assistant handled start command", zap.Int64("from_id", extractSenderID(msg)))
		_ = reply
	}
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
