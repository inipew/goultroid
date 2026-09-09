package client

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/settings"
	"go.uber.org/zap"
)

var (
	ErrBotTokenRequired = errors.New("assistant/client: BOT_TOKEN is required")
	ErrAlreadyRunning   = errors.New("assistant/client: client already running")
)

type Client interface {
	Start(context.Context) error
	Stop(context.Context) error
	IsRunning() bool
	Username() string
	StartTime() time.Time
}

type AssistantClient struct {
	appID         int
	appHash       string
	botToken      string
	logger        *zap.Logger
	startTime     time.Time
	self          *tg.User
	mu            sync.RWMutex
	cancel        context.CancelFunc
	runDone       chan struct{}
	ready         chan struct{}
	startupResult chan error
	lastError     error
	shuttingDown  atomic.Bool
	lifecycle     *Lifecycle
	rateLimiter   RateLimiter
	cache         *peer.MemoryCache
	resolver      *peer.DefaultResolver
	interaction   *interaction.ClientInteraction
	cmdRouter     *command.Router
	cbRouter      *callback.Router
	menuCtrl      *menu.Controller
	metrics       core.MetricsCollector
	settingsSvc   *settings.Service
}

var _ Client = (*AssistantClient)(nil)

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
		appID: appID, appHash: appHash, botToken: botToken, logger: logger,
		startTime: time.Now(), lifecycle: NewLifecycle(), rateLimiter: rl,
		cache: cache, resolver: res, cmdRouter: cmdR, cbRouter: cbR, menuCtrl: ctrl,
	}
	ctrl.AttachRoutes(cbR, c.Username, c.StartTime)
	command.AttachDefaultCommandsWithStore(cmdR, c.Username, c.StartTime, presentation.RenderScreen, ctrl.Instances())
	return c
}

func (c *AssistantClient) Start(ctx context.Context) error {
	if c.botToken == "" {
		return ErrBotTokenRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.lifecycle.State() == StateStopped {
		c.mu.RLock()
		previousDone := c.runDone
		c.mu.RUnlock()
		if previousDone != nil {
			select {
			case <-previousDone:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if !c.lifecycle.TryStart() {
		return ErrAlreadyRunning
	}

	runCtx, cancel := context.WithCancel(ctx)
	runDone := make(chan struct{})
	ready := make(chan struct{})
	startupResult := make(chan error, 1)
	c.mu.Lock()
	c.cancel = cancel
	c.runDone = runDone
	c.ready = ready
	c.startupResult = startupResult
	c.lastError = nil
	c.startTime = time.Now()
	c.mu.Unlock()

	dispatcher := tg.NewUpdateDispatcher()
	updateMgr := updates.New(updates.Config{Handler: dispatcher})
	tdClient := telegram.NewClient(c.appID, c.appHash, telegram.Options{UpdateHandler: updateMgr})
	c.resolver.SetEntityFetcher(peer.NewTelegramEntityFetcher(tdClient.API()))
	c.interaction = interaction.NewClientInteraction(tdClient.API(), c.logger)
	if c.metrics != nil {
		c.interaction.SetMetricsCollector(c.metrics)
	}
	c.interaction.SetPeerReResolver(c.resolver)
	c.shuttingDown.Store(false)

	deps := UpdateHandlerDeps{
		Logger: c.logger, RateLimiter: c.rateLimiter, Resolver: c.resolver,
		CmdRouter: c.cmdRouter, CallbackRouter: c.cbRouter, Interaction: c.interaction,
		CacheEntities: c.CacheEntities, IsShuttingDown: c.shuttingDown.Load,
		MenuController: c.menuCtrl, SettingsService: c.settingsSvc,
	}
	RegisterUpdateHandlers(&dispatcher, deps)

	go func() {
		defer close(runDone)
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

			// Telegram's native command menu is generated from the canonical
			// Assistant command surface. Registration is best-effort so a
			// presentation API failure never prevents the bot from starting.
			if c.cmdRouter != nil {
				if err := menu.RegisterTelegramCommandMenu(ctx, tdClient.API(), c.cmdRouter.CoreRouter()); err != nil {
					c.logger.Warn("assistant: failed to register Telegram command menu", zap.Error(err))
				}
			}

			c.lifecycle.SetState(StateRunning)
			c.logger.Info("assistant client running", zap.String("username", user.Username))
			close(ready)
			return updateMgr.Run(ctx, tdClient.API(), user.ID, updates.AuthOptions{IsBot: true})
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			c.mu.Lock()
			c.lastError = err
			c.mu.Unlock()
			c.lifecycle.SetState(StateFailed)
			c.logger.Error("assistant client stopped with an error", zap.Error(err))
			startupResult <- err
		} else {
			c.lifecycle.SetState(StateStopped)
			startupResult <- err
		}
	}()

	// The assistant is optional. Launching it must not delay the primary Telegram
	// client; callers that require confirmed readiness can use WaitReady.
	return nil
}

func waitForStartup(ctx context.Context, ready <-chan struct{}, errCh <-chan error) error {
	select {
	case <-ready:
		return nil
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitReady waits until the current assistant run has authenticated and can
// receive updates, or returns its startup failure/caller cancellation.
func (c *AssistantClient) WaitReady(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.RLock()
	ready := c.ready
	startupResult := c.startupResult
	c.mu.RUnlock()
	if ready == nil || startupResult == nil {
		return errors.New("assistant client has not been started")
	}
	return waitForStartup(ctx, ready, startupResult)
}

func (c *AssistantClient) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	state := c.lifecycle.State()
	if state == StateNew {
		return nil
	}
	if state != StateStopping {
		_ = c.lifecycle.TryStop()
	}
	c.shuttingDown.Store(true)
	c.mu.Lock()
	cancel := c.cancel
	done := c.runDone
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		c.lifecycle.SetState(StateStopped)
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		c.logger.Warn("assistant: shutdown timed out waiting for client loop")
		return ctx.Err()
	}
}

func (c *AssistantClient) IsShuttingDown() bool { return c.shuttingDown.Load() }
func (c *AssistantClient) IsRunning() bool      { return c.lifecycle.State() == StateRunning }
func (c *AssistantClient) State() ClientState   { return c.lifecycle.State() }
func (c *AssistantClient) LastError() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastError
}
func (c *AssistantClient) Username() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.self != nil && c.self.Username != "" {
		return c.self.Username
	}
	return "GoUltroidBot"
}
func (c *AssistantClient) StartTime() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.startTime
}
func (c *AssistantClient) SetAuthorizer(auth callback.Authorizer) { c.cbRouter.SetAuthorizer(auth) }
func (c *AssistantClient) SetOwner(ownerID int64, sudoGetter func() []int64) {
	c.cbRouter.SetAuthorizer(callback.NewOwnerAuthorizer(ownerID, sudoGetter))
	if c.cmdRouter != nil {
		c.cmdRouter.SetOwner(ownerID, sudoGetter)
	}
}
func (c *AssistantClient) SetCoreRouter(router *core.Router) {
	if c.cmdRouter != nil {
		c.cmdRouter.SetCoreRouter(router)
	}
	if c.menuCtrl != nil {
		c.menuCtrl.SetCommandSource(router)
	}
}
func (c *AssistantClient) SetSettingsService(svc *settings.Service) {
	c.settingsSvc = svc
	if c.menuCtrl != nil && c.cbRouter != nil {
		c.menuCtrl.AttachSettingsRoutes(c.cbRouter, svc)
	}
}
func (c *AssistantClient) SetMetricsCollector(m core.MetricsCollector) {
	c.metrics = m
	if c.cmdRouter != nil {
		c.cmdRouter.SetMetricsCollector(m)
	}
	if c.cbRouter != nil {
		c.cbRouter.SetMetricsCollector(m)
	}
	if c.interaction != nil {
		c.interaction.SetMetricsCollector(m)
	}
}
func (c *AssistantClient) CacheEntities(e tg.Entities) {
	if c.cache != nil {
		c.cache.CacheEntities(e)
	}
}
