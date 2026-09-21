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
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/assistant/presentation"
	assistentrpc "github.com/inipew/goultroid/internal/assistant/rpc"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	inlineService "github.com/inipew/goultroid/internal/services/inline"
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
	appID               int
	appHash             string
	botToken            string
	logger              *zap.Logger
	startTime           time.Time
	self                *tg.User
	mu                  sync.RWMutex
	lifecycleOpMu       sync.Mutex
	cancel              context.CancelFunc
	runDone             chan struct{}
	ready               chan struct{}
	startupResult       chan error
	lastError           error
	shuttingDown        atomic.Bool
	lifecycle           *Lifecycle
	rateLimiter         RateLimiter
	cache               *peer.MemoryCache
	resolver            *peer.DefaultResolver
	interaction         *interaction.ClientInteraction
	cmdRouter           *command.Router
	cbRouter            *callback.Router
	menuCtrl            *menu.Controller
	metrics             core.MetricsCollector
	ownerID             int64
	sudoGetter          func() []int64
	settingsSvc         *settings.Service
	tasks               tasks.Client
	delayedActions      core.DelayedActionScheduler
	pluginScopeResolver func(string) (tasks.ScopeIdentity, bool)
	inlineEngine        *inlineService.Engine
	rpcExecutor         assistentrpc.Executor
	v2Catalog           feature.Catalog
	v2Sessions          *rootinteraction.Runtime
	v2Actions           *rootinteraction.Dispatcher
	v2Ingress           *v2Ingress
	legacyStart         command.Handler
	shellMu             sync.Mutex
	shellScope          tasks.ScopeIdentity
	shellRegistrations  []*rootinteraction.HandlerRegistration
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
		rpcExecutor: assistentrpc.DirectExecutor{},
	}
	ctrl.AttachRoutes(cbR, c.Username, c.StartTime)
	c.legacyStart = command.NewUnavailableStartHandler()
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
	inlineQueryService := newAssistantInlineQueryServicer(managedAPI)
	c.mu.RLock()
	v2Catalog := c.v2Catalog
	v2Sessions := c.v2Sessions
	v2Actions := c.v2Actions
	c.mu.RUnlock()
	var v2 *v2Ingress
	if v2Catalog != nil && v2Sessions != nil && v2Actions != nil {
		v2Service := newV2PresentationServicer(c.interaction)
		v2Engine, v2Err := orchestration.New(v2Sessions, v2Actions, presentationtelegram.NewBridge(v2Service))
		if v2Err != nil {
			startErr := fmt.Errorf("configure a2 interaction ingress: %w", v2Err)
			cancel()
			c.lifecycle.SetState(StateFailed)
			c.mu.Lock()
			c.lastError = startErr
			c.v2Ingress = nil
			c.mu.Unlock()
			startupResult <- startErr
			close(runDone)
			return startErr
		}
		v2 = &v2Ingress{engine: v2Engine, ack: v2Service, input: c.handleV2TextInput}
	}
	c.mu.Lock()
	c.v2Ingress = v2
	c.mu.Unlock()
	c.shuttingDown.Store(false)

	deps := UpdateHandlerDeps{
		Logger: c.logger, RateLimiter: c.rateLimiter, Resolver: c.resolver,
		CmdRouter: c.cmdRouter, CallbackRouter: c.cbRouter, Interaction: c.interaction,
		CacheEntities: c.CacheEntities, IsShuttingDown: c.shuttingDown.Load,
		LegacyTextInput: c.menuCtrl,
		InlineEngine: c.inlineEngine, InlineService: inlineQueryService, Tasks: c.tasks,
		V2Ingress: v2,
	}
	RegisterUpdateHandlers(&dispatcher, deps)

	go func() {
		defer func() {
			c.mu.Lock()
			c.v2Ingress = nil
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
				if err := menu.RegisterTelegramCommandMenu(ctx, managedAPI, c.cmdRouter.CoreRouter()); err != nil {
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
		c.v2Ingress = nil
		c.mu.Unlock()
		c.lifecycle.SetState(StateStopped)
		return nil
	}
	select {
	case <-done:
		c.mu.Lock()
		c.v2Ingress = nil
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
func (c *AssistantClient) CallbackRouter() *callback.Router {
	return c.cbRouter
}
func (c *AssistantClient) SetAuthorizer(auth callback.Authorizer) { c.cbRouter.SetAuthorizer(auth) }
func (c *AssistantClient) SetOwner(ownerID int64, sudoGetter func() []int64) {
	c.mu.Lock()
	c.ownerID = ownerID
	c.sudoGetter = sudoGetter
	c.mu.Unlock()
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
	c.v2Catalog = catalog
	c.v2Sessions = sessions
	c.v2Actions = actions
	c.mu.Unlock()
}
func (c *AssistantClient) SetInlineEngine(engine *inlineService.Engine) {
	c.mu.Lock()
	c.inlineEngine = engine
	c.mu.Unlock()
}
func (c *AssistantClient) SetSettingsService(svc *settings.Service) {
	c.mu.Lock()
	c.settingsSvc = svc
	c.mu.Unlock()
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

func (c *AssistantClient) LegacyMenuCompatibility() menu.CompatibilityHost {
	return c.menuCtrl
}

func callbackOrderingKey(evt *core.CallbackQueryEvent) string {
	if evt == nil {
		return ""
	}
	if !evt.IsInline() {
		if evt.ChatID != 0 && evt.MsgID != 0 {
			return fmt.Sprintf("callback:msg:%d:%d", evt.ChatID, evt.MsgID)
		}
		if evt.MsgID != 0 {
			return fmt.Sprintf("callback:msg:%d", evt.MsgID)
		}
		return fmt.Sprintf("callback:%d", evt.QueryID)
	}
	if evt.Target.InlineID != nil {
		switch id := evt.Target.InlineID.(type) {
		case *tg.InputBotInlineMessageID:
			return fmt.Sprintf("callback:inline_msg:%d:%d", id.DCID, id.ID)
		case *tg.InputBotInlineMessageID64:
			return fmt.Sprintf("callback:inline_msg:%d:%d", id.DCID, id.ID)
		}
	}
	if evt.ChatInstance != 0 {
		return fmt.Sprintf("callback:instance:%d", evt.ChatInstance)
	}
	return fmt.Sprintf("inline_callback:%d", evt.QueryID)
}

// SetCallbackRouter wires a CoreCallbackDispatcher as fallback handler on the assistant callback router.
func (c *AssistantClient) SetCallbackRouter(coreRouter CoreCallbackDispatcher) {
	if c.cbRouter == nil || coreRouter == nil {
		return
	}
	c.cbRouter.SetFallbackHandler(func(ctx context.Context, tx *callback.Transaction) error {
		if !coreRouter.HasHandler(tx.Payload.Namespace) {
			_ = tx.Answer(ctx, "Unknown button action", false)
			return fmt.Errorf("%w: %s:%s", callback.ErrUnknownAction, tx.Payload.Namespace, tx.Payload.Action)
		}
		rawData := tx.RawData
		if len(rawData) == 0 {
			if tx.Payload.State != "" {
				rawData = []byte(fmt.Sprintf("%s:%s:%s:%s", tx.Payload.Version, tx.Payload.Namespace, tx.Payload.Action, tx.Payload.State))
			} else {
				rawData = []byte(fmt.Sprintf("%s:%s:%s", tx.Payload.Version, tx.Payload.Namespace, tx.Payload.Action))
			}
		}

		c.mu.RLock()
		scopeResolver := c.pluginScopeResolver
		taskClient := c.tasks
		c.mu.RUnlock()

		scope, available := coreRouter.TaskScope(rawData, scopeResolver)
		if !available {
			_ = tx.Answer(ctx, "Feature not available.", false)
			return fmt.Errorf("%w: feature not available for %s", callback.ErrUnknownAction, tx.Payload.Namespace)
		}

		evt := &core.CallbackQueryEvent{
			At:      time.Now(),
			QueryID: tx.QueryID,
			UserID:  tx.UserID,
			ChatID:  tx.Target.ChatID(),
			MsgID:   tx.Target.MessageID(),
			Data:    rawData,
			Origin:  core.CallbackOriginMessage,
			Target: core.CallbackTarget{
				Origin:       core.CallbackOriginMessage,
				Peer:         tx.Target.Peer(),
				MessageID:    tx.Target.MessageID(),
				ChatInstance: tx.Target.ChatInstance(),
			},
			ChatInstance: tx.Target.ChatInstance(),
		}
		svc := &assistantCallbackServicer{tx: tx}

		if taskClient != nil {
			taskID := fmt.Sprintf("asst:cb:%d", evt.QueryID)
			owner := fmt.Sprintf("telegram:user:%d", evt.UserID)
			doneCh := make(chan error, 1)
			ticket, err := taskClient.Submit(ctx, tasks.WorkSpec{
				ID:               tasks.TaskID(taskID),
				Scope:            scope,
				QuotaOwner:       tasks.OwnerID(owner),
				Pool:             "interactive",
				Class:            tasks.PriorityInteractive,
				OrderingKey:      callbackOrderingKey(evt),
				ExecutionTimeout: 15 * time.Second,
				Handler: func(taskCtx context.Context) error {
					dErr := coreRouter.Dispatch(taskCtx, evt, svc)
					doneCh <- dErr
					return dErr
				},
			})
			if err != nil {
				_ = tx.Answer(ctx, "Interaction busy. Please retry.", true)
				return fmt.Errorf("task submission failed: %w", err)
			}
			var ticketDone <-chan struct{}
			if ticket != nil {
				ticketDone = ticket.Done()
			}
			select {
			case dErr := <-doneCh:
				return dErr
			case <-ticketDone:
				select {
				case dErr := <-doneCh:
					return dErr
				default:
				}
				if ticket != nil {
					res, _ := ticket.Result()
					if res.IsSuccess() {
						return nil
					}
					if res.Failure.Message != "" {
						return errors.New(res.Failure.Message)
					}
					return fmt.Errorf("task finished with outcome %s (%s)", res.Outcome, res.Cause)
				}
				return nil
			case <-ctx.Done():
				if ticket != nil {
					_, _ = taskClient.Cancel(ticket.TaskID(), tasks.CauseTimeout)
				}
				return ctx.Err()
			}
		}

		_ = tx.Answer(ctx, "Interaction service unavailable.", true)
		return ErrCallbackTasksNotConfigured
	})

	c.cbRouter.SetFallbackInlineHandler(func(ctx context.Context, tx *callback.InlineTransaction) error {
		if !coreRouter.HasHandler(tx.Payload.Namespace) {
			_ = tx.Answer(ctx, "Action no longer available", false)
			return fmt.Errorf("%w: %s:%s", callback.ErrUnknownAction, tx.Payload.Namespace, tx.Payload.Action)
		}
		rawData := tx.RawData
		if len(rawData) == 0 {
			if tx.Payload.State != "" {
				rawData = []byte(fmt.Sprintf("%s:%s:%s:%s", tx.Payload.Version, tx.Payload.Namespace, tx.Payload.Action, tx.Payload.State))
			} else {
				rawData = []byte(fmt.Sprintf("%s:%s:%s", tx.Payload.Version, tx.Payload.Namespace, tx.Payload.Action))
			}
		}

		c.mu.RLock()
		scopeResolver := c.pluginScopeResolver
		taskClient := c.tasks
		c.mu.RUnlock()

		scope, available := coreRouter.TaskScope(rawData, scopeResolver)
		if !available {
			_ = tx.Answer(ctx, "Feature not available.", false)
			return fmt.Errorf("%w: feature not available for %s", callback.ErrUnknownAction, tx.Payload.Namespace)
		}

		evt := &core.CallbackQueryEvent{
			At:      time.Now(),
			QueryID: tx.QueryID,
			UserID:  tx.UserID,
			ChatID:  0,
			MsgID:   0,
			Data:    rawData,
			Origin:  core.CallbackOriginInline,
			Target: core.CallbackTarget{
				Origin:       core.CallbackOriginInline,
				InlineID:     tx.Target.MessageID(),
				ChatInstance: tx.Target.ChatInstance(),
			},
			ChatInstance: tx.Target.ChatInstance(),
		}
		svc := &assistantInlineCallbackServicer{tx: tx}

		if taskClient != nil {
			taskID := fmt.Sprintf("asst:cb:inline:%d", evt.QueryID)
			owner := fmt.Sprintf("telegram:user:%d", evt.UserID)
			doneCh := make(chan error, 1)
			ticket, err := taskClient.Submit(ctx, tasks.WorkSpec{
				ID:               tasks.TaskID(taskID),
				Scope:            scope,
				QuotaOwner:       tasks.OwnerID(owner),
				Pool:             "interactive",
				Class:            tasks.PriorityInteractive,
				OrderingKey:      callbackOrderingKey(evt),
				ExecutionTimeout: 15 * time.Second,
				Handler: func(taskCtx context.Context) error {
					dErr := coreRouter.Dispatch(taskCtx, evt, svc)
					doneCh <- dErr
					return dErr
				},
			})
			if err != nil {
				_ = tx.Answer(ctx, "Interaction busy. Please retry.", true)
				return fmt.Errorf("task submission failed: %w", err)
			}
			var ticketDone <-chan struct{}
			if ticket != nil {
				ticketDone = ticket.Done()
			}
			select {
			case dErr := <-doneCh:
				return dErr
			case <-ticketDone:
				select {
				case dErr := <-doneCh:
					return dErr
				default:
				}
				if ticket != nil {
					res, _ := ticket.Result()
					if res.IsSuccess() {
						return nil
					}
					if res.Failure.Message != "" {
						return errors.New(res.Failure.Message)
					}
					return fmt.Errorf("task finished with outcome %s (%s)", res.Outcome, res.Cause)
				}
				return nil
			case <-ctx.Done():
				if ticket != nil {
					_, _ = taskClient.Cancel(ticket.TaskID(), tasks.CauseTimeout)
				}
				return ctx.Err()
			}
		}

		_ = tx.Answer(ctx, "Interaction service unavailable.", true)
		return ErrCallbackTasksNotConfigured
	})
}
