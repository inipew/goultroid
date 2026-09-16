package client

import (
	"context"
	"errors"
	"fmt"
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
	settingsSvc         *settings.Service
	tasks               tasks.Client
	pluginScopeResolver func(string) (tasks.ScopeIdentity, bool)
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
func (c *AssistantClient) CallbackRouter() *callback.Router {
	return c.cbRouter
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
func (c *AssistantClient) SetTasks(client tasks.Client) {
	c.mu.Lock()
	c.tasks = client
	c.mu.Unlock()
	if c.cmdRouter != nil {
		c.cmdRouter.SetTasks(client)
	}
}
func (c *AssistantClient) SetPluginScopeResolver(resolver func(string) (tasks.ScopeIdentity, bool)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pluginScopeResolver = resolver
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
