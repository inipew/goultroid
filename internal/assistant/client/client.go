package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	assistantdeeplink "github.com/inipew/goultroid/internal/assistant/deeplink"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/peer"
	assistentrpc "github.com/inipew/goultroid/internal/assistant/rpc"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	broadcastsvc "github.com/inipew/goultroid/internal/services/broadcast"
	inlineService "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

var (
	ErrBotTokenRequired           = errors.New("assistant/client: BOT_TOKEN is required")
	ErrAlreadyRunning             = errors.New("assistant/client: client already running")
	ErrCallbackTasksNotConfigured = errors.New("assistant/client: task client is required for callback execution")
)

type Client interface {
	Start(context.Context) error
	Stop(context.Context) error
	IsRunning() bool
	Username() string
	StartTime() time.Time
}

type AssistantClient struct {
	appID                 int
	appHash               string
	botToken              string
	logger                *zap.Logger
	startTime             time.Time
	self                  *tg.User
	mu                    sync.RWMutex
	lifecycleOpMu         sync.Mutex
	cancel                context.CancelFunc
	runDone               chan struct{}
	ready                 chan struct{}
	startupResult         chan error
	lastError             error
	shuttingDown          atomic.Bool
	lifecycle             *Lifecycle
	rateLimiter           RateLimiter
	cache                 *peer.MemoryCache
	resolver              *peer.DefaultResolver
	interaction           *interaction.ClientInteraction
	cmdRouter             *command.Router
	callbackDispatcher    CoreCallbackDispatcher
	callbackDeduper       *callbackQueryDeduper
	metrics               core.MetricsCollector
	ownerID               int64
	sudoGetter            func() []int64
	settingsSvc           *settings.Service
	tasks                 tasks.Client
	delayedActions        core.DelayedActionScheduler
	pluginScopeResolver   func(string) (tasks.ScopeIdentity, bool)
	inlineEngine          *inlineService.Engine
	deepLinks             *assistantdeeplink.Router
	pmRelay               pmrelay.Ingress
	audience              pmrelay.AudienceRegistry
	broadcast             *broadcastsvc.Service
	deepLinkSeq           atomic.Uint64
	rpcExecutor           assistentrpc.Executor
	featureCatalog        feature.Catalog
	interactionSessions   *rootinteraction.Runtime
	actionDispatcher      *rootinteraction.Dispatcher
	interactionIngress    *interactionIngress
	featureDrivers        map[string]interaction.FeatureDriver
	featureDriverCleanups []func()
	shellMu               sync.Mutex
	shellScope            tasks.ScopeIdentity
	shellRegistrations    []*rootinteraction.HandlerRegistration
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
	c := &AssistantClient{
		appID: appID, appHash: appHash, botToken: botToken, logger: logger,
		startTime: time.Now(), lifecycle: NewLifecycle(), rateLimiter: rl,
		cache: cache, resolver: res, cmdRouter: cmdR,
		callbackDeduper: newCallbackQueryDeduper(),
		rpcExecutor:     assistentrpc.DirectExecutor{},
	}
	cmdR.Register("/start", c.dispatchStart)
	return c
}

// SetRPCExecutor installs the application-owned Telegram executor before Start.
func (c *AssistantClient) SetRPCExecutor(executor assistentrpc.Executor) {
	if executor == nil {
		return
	}
	c.mu.Lock()
	c.rpcExecutor = executor
	c.mu.Unlock()
}

func (c *AssistantClient) Start(ctx context.Context) error {
	c.lifecycleOpMu.Lock()
	defer c.lifecycleOpMu.Unlock()

	if c.botToken == "" {
		return ErrBotTokenRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state := c.lifecycle.State()
	if state == StateStarting || state == StateRunning || state == StateStopping {
		return ErrAlreadyRunning
	}
	if state == StateStopped || state == StateFailed {
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
	managedAPI := &managedAPI{raw: tdClient.API(), executor: c.rpcExecutor}
	c.resolver.SetEntityFetcher(peer.NewTelegramEntityFetcher(managedAPI))
	c.interaction = interaction.NewClientInteraction(managedAPI, c.logger)
	c.interaction.SetRPCExecutor(c.rpcExecutor)
	c.interaction.SetMediaSender(message.NewSender(tdClient.API()), uploader.NewUploader(tdClient.API()))
	if c.metrics != nil {
		c.interaction.SetMetricsCollector(c.metrics)
	}
	c.interaction.SetPeerReResolver(c.resolver)
	inlineQueryService := newAssistantInlineQueryServicer(managedAPI, c.interaction)
	c.mu.RLock()
	featureCatalog := c.featureCatalog
	interactionSessions := c.interactionSessions
	actionDispatcher := c.actionDispatcher
	callbackDispatcher := c.callbackDispatcher
	callbackDeduper := c.callbackDeduper
	pluginScopeResolver := c.pluginScopeResolver
	taskClient := c.tasks
	pmRelay := c.pmRelay
	audience := c.audience
	c.mu.RUnlock()
	var ingress *interactionIngress
	if featureCatalog != nil && interactionSessions != nil && actionDispatcher != nil {
		presentationService := newInteractionPresentationServicer(c.interaction)
		interactionEngine, interactionErr := orchestration.New(interactionSessions, actionDispatcher, presentationtelegram.NewBridge(presentationService))
		if interactionErr != nil {
			startErr := fmt.Errorf("configure interaction ingress: %w", interactionErr)
			cancel()
			c.lifecycle.SetState(StateFailed)
			c.mu.Lock()
			c.lastError = startErr
			c.interactionIngress = nil
			c.mu.Unlock()
			startupResult <- startErr
			close(runDone)
			return startErr
		}
		if bindErr := c.bindFeatureDrivers(interactionEngine, featureCatalog, presentationService); bindErr != nil {
			startErr := fmt.Errorf("bind feature drivers: %w", bindErr)
			cancel()
			c.lifecycle.SetState(StateFailed)
			c.mu.Lock()
			c.lastError = startErr
			c.interactionIngress = nil
			c.mu.Unlock()
			startupResult <- startErr
			close(runDone)
			return startErr
		}
		ingress = &interactionIngress{engine: interactionEngine, ack: presentationService, tasks: taskClient, input: c.handleInteractionTextInput}
	}
	c.mu.Lock()
	c.interactionIngress = ingress
	c.mu.Unlock()
	c.shuttingDown.Store(false)

	relayTransport := newTelegramRelayVisitorTransport(c.resolver, c.interaction)
	relayIngress := NewRelayIngress(pmRelay, taskClient, relayTransport)
	if relayIngress != nil {
		if forceSubPolicy, ok := pmRelay.(pmrelay.ForceSubPolicy); ok {
			relayIngress.setForceSubGate(newTelegramForceSubGate(
				forceSubPolicy,
				managedAPI,
				c.resolver,
				c.logger,
			))
		}
	}

	deps := UpdateHandlerDeps{
		Logger: c.logger, RateLimiter: c.rateLimiter, Resolver: c.resolver,
		CmdRouter: c.cmdRouter, CallbackDispatcher: callbackDispatcher, CallbackDeduper: callbackDeduper,
		Interaction: c.interaction, CacheEntities: c.CacheEntities, IsShuttingDown: c.shuttingDown.Load,
		InlineEngine: c.inlineEngine, InlineService: inlineQueryService, Tasks: taskClient,
		PluginScopeResolver: pluginScopeResolver, InteractionIngress: ingress,
		RelayIngress: relayIngress,
		AudienceRegistry: audience,
	}
	RegisterUpdateHandlers(&dispatcher, deps)

	go func() {
		defer func() {
			c.unbindFeatureDrivers()
			c.mu.Lock()
			c.interactionIngress = nil
			c.mu.Unlock()
			close(runDone)
		}()
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
				if err := command.RegisterTelegramCommandMenu(ctx, managedAPI, c.cmdRouter.CoreRouter()); err != nil {
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
	c.lifecycleOpMu.Lock()
	defer c.lifecycleOpMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	state := c.lifecycle.State()
	if state == StateNew || state == StateStopped {
		return nil
	}
	if state == StateStarting || state == StateRunning {
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
		c.mu.Lock()
		c.interactionIngress = nil
		c.mu.Unlock()
		c.lifecycle.SetState(StateStopped)
		return nil
	}
	select {
	case <-done:
		c.mu.Lock()
		c.interactionIngress = nil
		c.mu.Unlock()
		c.lifecycle.SetState(StateStopped)
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
func (c *AssistantClient) SetOwner(ownerID int64, sudoGetter func() []int64) {
	c.mu.Lock()
	c.ownerID = ownerID
	c.sudoGetter = sudoGetter
	c.mu.Unlock()
	if c.cmdRouter != nil {
		c.cmdRouter.SetOwner(ownerID, sudoGetter)
	}
}
func (c *AssistantClient) SetCoreRouter(router *core.Router) {
	if c.cmdRouter != nil {
		c.cmdRouter.SetCoreRouter(router)
	}
}
func (c *AssistantClient) SetTasks(client tasks.Client) {
	c.mu.Lock()
	c.tasks = client
	c.mu.Unlock()
	if c.cmdRouter != nil {
		c.cmdRouter.SetTasks(client)
	}
}
func (c *AssistantClient) SetDelayedActions(scheduler core.DelayedActionScheduler) {
	c.mu.Lock()
	c.delayedActions = scheduler
	c.mu.Unlock()
	if c.cmdRouter != nil {
		c.cmdRouter.SetDelayedActions(scheduler)
	}
}
func (c *AssistantClient) SetPluginScopeResolver(resolver func(string) (tasks.ScopeIdentity, bool)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pluginScopeResolver = resolver
}

func (c *AssistantClient) SetInteractionFoundation(catalog feature.Catalog, sessions *rootinteraction.Runtime, actions *rootinteraction.Dispatcher) {
	c.mu.Lock()
	c.featureCatalog = catalog
	c.interactionSessions = sessions
	c.actionDispatcher = actions
	c.mu.Unlock()
}
func (c *AssistantClient) SetInlineEngine(engine *inlineService.Engine) {
	c.mu.Lock()
	c.inlineEngine = engine
	c.mu.Unlock()
}
func (c *AssistantClient) SetDeepLinkRouter(router *assistantdeeplink.Router) {
	c.mu.Lock()
	c.deepLinks = router
	c.mu.Unlock()
}
func (c *AssistantClient) SetRelayIngress(relay pmrelay.Ingress) {
	c.mu.Lock()
	c.pmRelay = relay
	c.mu.Unlock()
}
func (c *AssistantClient) SetAudienceRegistry(registry pmrelay.AudienceRegistry) {
	c.mu.Lock()
	c.audience = registry
	c.mu.Unlock()
}
func (c *AssistantClient) SetBroadcastService(service *broadcastsvc.Service) {
	c.mu.Lock()
	c.broadcast = service
	c.mu.Unlock()
}
func (c *AssistantClient) SetSettingsService(svc *settings.Service) {
	c.mu.Lock()
	c.settingsSvc = svc
	c.mu.Unlock()
}

func (c *AssistantClient) SetSavedResponseBindings(bindings *savedresponse.BindingService, delivery *savedresponse.ResponseDelivery) {
	if c.cmdRouter != nil {
		c.cmdRouter.SetSavedResponseBindings(bindings, delivery)
	}
}
func (c *AssistantClient) SetMetricsCollector(m core.MetricsCollector) {
	c.metrics = m
	if c.cmdRouter != nil {
		c.cmdRouter.SetMetricsCollector(m)
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

// SetCallbackRouter installs the canonical core/plugin callback dispatcher.
func (c *AssistantClient) SetCallbackRouter(coreRouter CoreCallbackDispatcher) {
	c.mu.Lock()
	c.callbackDispatcher = coreRouter
	c.mu.Unlock()
}
