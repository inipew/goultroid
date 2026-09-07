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
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	IsRunning() bool
	Username() string
	StartTime() time.Time
}

type AssistantClient struct {
	appID     int
	appHash   string
	botToken  string
	logger    *zap.Logger
	startTime time.Time
	self      *tg.User
	mu        sync.RWMutex
	cancel    context.CancelFunc
	shuttingDown atomic.Bool
	wg           sync.WaitGroup

	lifecycle   *Lifecycle
	rateLimiter RateLimiter
	cache       *peer.MemoryCache
	resolver    *peer.DefaultResolver
	interaction *interaction.ClientInteraction
	cmdRouter   *command.Router
	cbRouter    *callback.Router
	menuCtrl    *menu.Controller
	metrics     core.MetricsCollector
}

var _ Client = (*AssistantClient)(nil)

func NewAssistantClient(appID int, appHash string, botToken string, logger *zap.Logger) *AssistantClient {
	if logger == nil { logger = zap.NewNop() }
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
	if c.botToken == "" { return ErrBotTokenRequired }
	if !c.lifecycle.TryStart() { return ErrAlreadyRunning }
	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock(); c.cancel = cancel; c.startTime = time.Now(); c.mu.Unlock()
	dispatcher := tg.NewUpdateDispatcher()
	updateMgr := updates.New(updates.Config{Handler: dispatcher})
	tdClient := telegram.NewClient(c.appID, c.appHash, telegram.Options{UpdateHandler: updateMgr})
	entityFetcher := peer.NewTelegramEntityFetcher(tdClient.API())
	c.resolver.SetEntityFetcher(entityFetcher)
	c.interaction = interaction.NewClientInteraction(tdClient.API(), c.logger)
	if c.metrics != nil { c.interaction.SetMetricsCollector(c.metrics) }
	c.interaction.SetPeerReResolver(c.resolver)
	c.shuttingDown.Store(false)
	deps := UpdateHandlerDeps{Logger: c.logger, RateLimiter: c.rateLimiter, Resolver: c.resolver, CmdRouter: c.cmdRouter, CallbackRouter: c.cbRouter, Interaction: c.interaction, CacheEntities: c.CacheEntities, IsShuttingDown: c.shuttingDown.Load}
	RegisterUpdateHandlers(&dispatcher, deps)
	errCh := make(chan error, 1)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		err := tdClient.Run(runCtx, func(ctx context.Context) error {
			status, err := tdClient.Auth().Status(ctx); if err != nil { return err }
			if !status.Authorized { if _, err := tdClient.Auth().Bot(ctx, c.botToken); err != nil { return err } }
			user, err := tdClient.Self(ctx); if err != nil { return err }
			c.mu.Lock(); c.self = user; c.mu.Unlock()
			c.lifecycle.SetState(StateRunning)
			c.logger.Info("assistant client running", zap.String("username", user.Username))
			return updateMgr.Run(ctx, tdClient.API(), user.ID, updates.AuthOptions{IsBot: true})
		})
		if err != nil && !errors.Is(err, context.Canceled) { c.lifecycle.SetState(StateFailed); errCh <- err } else { c.lifecycle.SetState(StateStopped); errCh <- nil }
	}()
	select { case err := <-errCh: return err; case <-time.After(100 * time.Millisecond): return nil; case <-runCtx.Done(): return runCtx.Err() }
}

func (c *AssistantClient) Stop(ctx context.Context) error {
	if !c.lifecycle.TryStop() { return nil }
	c.shuttingDown.Store(true)
	c.mu.Lock(); if c.cancel != nil { c.cancel(); c.cancel = nil }; c.mu.Unlock()
	stopped := make(chan struct{})
	go func() { c.wg.Wait(); close(stopped) }()
	select { case <-stopped: case <-ctx.Done(): c.logger.Warn("assistant: shutdown timed out waiting for client loop") }
	c.lifecycle.SetState(StateStopped)
	return nil
}

func (c *AssistantClient) IsShuttingDown() bool { return c.shuttingDown.Load() }
func (c *AssistantClient) IsRunning() bool { return c.lifecycle.State() == StateRunning }
func (c *AssistantClient) Username() string { c.mu.RLock(); defer c.mu.RUnlock(); if c.self != nil && c.self.Username != "" { return c.self.Username }; return "GoUltroidBot" }
func (c *AssistantClient) StartTime() time.Time { c.mu.RLock(); defer c.mu.RUnlock(); return c.startTime }
func (c *AssistantClient) SetAuthorizer(auth callback.Authorizer) { c.cbRouter.SetAuthorizer(auth) }
func (c *AssistantClient) SetOwner(ownerID int64, sudoGetter func() []int64) { c.cbRouter.SetAuthorizer(callback.NewOwnerAuthorizer(ownerID, sudoGetter)); if c.cmdRouter != nil { c.cmdRouter.SetOwner(ownerID, sudoGetter) } }
func (c *AssistantClient) SetCoreRouter(router *core.Router) { if c.cmdRouter != nil { c.cmdRouter.SetCoreRouter(router) }; if c.menuCtrl != nil { c.menuCtrl.SetCommandSource(router) } }
func (c *AssistantClient) SetSettingsService(svc *settings.Service) { if c.menuCtrl != nil { c.menuCtrl.AttachSettingsRoutes(c.cbRouter, svc) } }
func (c *AssistantClient) SetMetricsCollector(m core.MetricsCollector) { c.metrics = m; if c.cmdRouter != nil { c.cmdRouter.SetMetricsCollector(m) }; if c.cbRouter != nil { c.cbRouter.SetMetricsCollector(m) }; if c.interaction != nil { c.interaction.SetMetricsCollector(m) } }
func (c *AssistantClient) CacheEntities(e tg.Entities) { if c.cache != nil { c.cache.CacheEntities(e) } }
