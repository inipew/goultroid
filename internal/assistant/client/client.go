package client

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/assistant/presentation"
	"go.uber.org/zap"
)

var (
	// ErrBotTokenRequired indicates that an assistant cannot start without a bot token.
	ErrBotTokenRequired = errors.New("assistant/client: BOT_TOKEN is required")
	// ErrAlreadyRunning indicates an attempt to start an already running assistant client.
	ErrAlreadyRunning = errors.New("assistant/client: client already running")
)

// Client defines the lifecycle and metadata methods for the assistant bot client.
type Client interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	IsRunning() bool
	Username() string
	StartTime() time.Time
}

// AssistantClient implements Client managing MTProto bot updates and subsystem routers.
type AssistantClient struct {
	appID     int
	appHash   string
	botToken  string
	logger    *zap.Logger
	startTime time.Time
	self      *tg.User
	mu        sync.RWMutex
	cancel    context.CancelFunc

	lifecycle   *Lifecycle
	rateLimiter RateLimiter
	cache       *peer.MemoryCache
	resolver    *peer.DefaultResolver
	interaction *interaction.ClientInteraction
	cmdRouter   *command.Router
	cbRouter    *callback.Router
	menuCtrl    *menu.Controller
}

var _ Client = (*AssistantClient)(nil)

// NewAssistantClient creates an initialized AssistantClient instance.
func NewAssistantClient(appID int, appHash string, botToken string, logger *zap.Logger) *AssistantClient {
	if logger == nil {
		logger = zap.NewNop()
	}

	cache := peer.NewMemoryCache()
	res := peer.NewResolver(cache)
	rl := NewUserRateLimiter(5, 2*time.Second)
	cmdR := command.NewRouter(logger)
	cbR := callback.NewRouter(logger)
	ctrl := menu.NewController(presentation.RenderScreen)

	c := &AssistantClient{
		appID:       appID,
		appHash:     appHash,
		botToken:    botToken,
		logger:      logger,
		startTime:   time.Now(),
		lifecycle:   NewLifecycle(),
		rateLimiter: rl,
		cache:       cache,
		resolver:    res,
		cmdRouter:   cmdR,
		cbRouter:    cbR,
		menuCtrl:    ctrl,
	}

	ctrl.AttachRoutes(cbR, c.Username, c.StartTime)
	command.AttachDefaultCommandsWithStore(cmdR, c.Username, c.StartTime, presentation.RenderScreen, ctrl.Instances())

	return c
}

// Start launches the Telegram MTProto client and handles updates.
func (c *AssistantClient) Start(ctx context.Context) error {
	if c.botToken == "" {
		return ErrBotTokenRequired
	}
	if !c.lifecycle.TryStart() {
		return ErrAlreadyRunning
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.startTime = time.Now()
	c.mu.Unlock()

	dispatcher := tg.NewUpdateDispatcher()
	updateMgr := updates.New(updates.Config{
		Handler: dispatcher,
	})

	tdClient := telegram.NewClient(c.appID, c.appHash, telegram.Options{
		UpdateHandler: updateMgr,
	})

	c.interaction = interaction.NewClientInteraction(tdClient.API(), c.logger)
	c.interaction.SetPeerReResolver(c.resolver)

	// Register updates
	deps := UpdateHandlerDeps{
		Logger:         c.logger,
		RateLimiter:    c.rateLimiter,
		Resolver:       c.resolver,
		CmdRouter:      c.cmdRouter,
		CallbackRouter: c.cbRouter,
		Interaction:    c.interaction,
		CacheEntities:  c.CacheEntities,
	}
	RegisterUpdateHandlers(&dispatcher, deps)

	errCh := make(chan error, 1)
	go func() {
		err := tdClient.Run(runCtx, func(ctx context.Context) error {
			status, err := tdClient.Auth().Status(ctx)
			if err != nil {
				return err
			}
			if !status.Authorized {
				if _, err := tdClient.Auth().Bot(ctx, c.botToken); err != nil {
					return err
				}
			}

			user, err := tdClient.Self(ctx)
			if err != nil {
				return err
			}
			c.mu.Lock()
			c.self = user
			c.mu.Unlock()

			c.lifecycle.SetState(StateRunning)
			c.logger.Info("assistant client running", zap.String("username", user.Username))
			return updateMgr.Run(ctx, tdClient.API(), user.ID, updates.AuthOptions{
				IsBot: true,
			})
		})

		if err != nil && !errors.Is(err, context.Canceled) {
			c.lifecycle.SetState(StateFailed)
			errCh <- err
		} else {
			c.lifecycle.SetState(StateStopped)
			errCh <- nil
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-time.After(100 * time.Millisecond):
		return nil
	case <-runCtx.Done():
		return runCtx.Err()
	}
}

// Stop gracefully shuts down the assistant client.
func (c *AssistantClient) Stop(ctx context.Context) error {
	if !c.lifecycle.TryStop() {
		return nil
	}
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.mu.Unlock()
	c.lifecycle.SetState(StateStopped)
	return nil
}

// IsRunning reports whether the client is active.
func (c *AssistantClient) IsRunning() bool {
	return c.lifecycle.State() == StateRunning
}

// Username returns the authenticated bot username.
func (c *AssistantClient) Username() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.self != nil && c.self.Username != "" {
		return c.self.Username
	}
	return "GoUltroidBot"
}

// StartTime returns when the assistant client was launched.
func (c *AssistantClient) StartTime() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.startTime
}

// SetAuthorizer configures the access control policy on the callback router.
func (c *AssistantClient) SetAuthorizer(auth callback.Authorizer) {
	c.cbRouter.SetAuthorizer(auth)
}

// CacheEntities caches users and chats from incoming updates into the peer cache.
func (c *AssistantClient) CacheEntities(e tg.Entities) {
	if c.cache != nil {
		c.cache.CacheEntities(e)
	}
}
