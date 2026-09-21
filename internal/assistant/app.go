package assistant

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/client"
	"github.com/inipew/goultroid/internal/assistant/menu"
	assistentrpc "github.com/inipew/goultroid/internal/assistant/rpc"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type Client interface {
	runtime.Component
	IsRunning() bool
	Username() string
	StartTime() time.Time
	SetCoreRouter(router *core.Router)
	SetOwner(ownerID int64, sudoGetter func() []int64)
	SetSettingsService(svc *settings.Service)
	SetMetricsCollector(m core.MetricsCollector)
	SetCallbackRouter(router client.CoreCallbackDispatcher)
	SetInlineEngine(engine *inline.Engine)
	SetTasks(client tasks.Client)
	SetPluginScopeResolver(resolver func(string) (tasks.ScopeIdentity, bool))
	SetInteractionFoundation(catalog feature.Catalog, sessions *rootinteraction.Runtime, actions *rootinteraction.Dispatcher)
	SetRPCExecutor(executor assistentrpc.Executor)
	LegacyMenuCompatibility() menu.CompatibilityHost
}

type AssistantApp struct {
	client *client.AssistantClient
	logger *zap.Logger
}

var _ Client = (*AssistantApp)(nil)
var _ runtime.CriticalComponent = (*AssistantApp)(nil)

func (a *AssistantApp) Name() string {
	return "assistant"
}

func (a *AssistantApp) Dependencies() []string {
	return []string{"dispatcher", "settings", "taskengine"}
}

func (a *AssistantApp) IsCritical() bool {
	return false
}

func (a *AssistantApp) Health(ctx context.Context) runtime.ComponentHealth {
	switch state := a.client.State(); state {
	case client.StateRunning:
		return runtime.ComponentHealth{Status: runtime.HealthHealthy}
	case client.StateFailed:
		err := a.client.LastError()
		return runtime.ComponentHealth{Status: runtime.HealthUnhealthy, Details: "assistant startup or runtime failed", Error: err}
	case client.StateStarting:
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "assistant is starting asynchronously"}
	case client.StateStopping:
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "assistant is stopping"}
	default:
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "assistant not running"}
	}
}

func NewApp(appID int, appHash string, botToken string, logger *zap.Logger) *AssistantApp {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &AssistantApp{client: client.NewAssistantClient(appID, appHash, botToken, logger), logger: logger}
}
func NewBotClient(appID int, appHash string, botToken string, logger *zap.Logger) *AssistantApp {
	return NewApp(appID, appHash, botToken, logger)
}
func (a *AssistantApp) Start(ctx context.Context) error        { return a.client.Start(ctx) }
func (a *AssistantApp) Stop(ctx context.Context) error         { return a.client.Stop(ctx) }
func (a *AssistantApp) IsRunning() bool                        { return a.client.IsRunning() }
func (a *AssistantApp) WaitReady(ctx context.Context) error    { return a.client.WaitReady(ctx) }
func (a *AssistantApp) Username() string                       { return a.client.Username() }
func (a *AssistantApp) StartTime() time.Time                   { return a.client.StartTime() }
func (a *AssistantApp) SetAuthorizer(auth callback.Authorizer) { a.client.SetAuthorizer(auth) }
func (a *AssistantApp) SetOwner(ownerID int64, sudoGetter func() []int64) {
	a.client.SetOwner(ownerID, sudoGetter)
}
func (a *AssistantApp) SetCoreRouter(router *core.Router) { a.client.SetCoreRouter(router) }
func (a *AssistantApp) SetTasks(client tasks.Client)      { a.client.SetTasks(client) }
func (a *AssistantApp) SetDelayedActions(scheduler core.DelayedActionScheduler) {
	a.client.SetDelayedActions(scheduler)
}
func (a *AssistantApp) SetSettingsService(svc *settings.Service)    { a.client.SetSettingsService(svc) }
func (a *AssistantApp) SetMetricsCollector(m core.MetricsCollector) { a.client.SetMetricsCollector(m) }
func (a *AssistantApp) SetCallbackRouter(router client.CoreCallbackDispatcher) {
	a.client.SetCallbackRouter(router)
}
func (a *AssistantApp) SetInlineEngine(engine *inline.Engine) { a.client.SetInlineEngine(engine) }
func (a *AssistantApp) SetRPCExecutor(executor assistentrpc.Executor) {
	a.client.SetRPCExecutor(executor)
}
func (a *AssistantApp) SetPluginScopeResolver(resolver func(string) (tasks.ScopeIdentity, bool)) {
	a.client.SetPluginScopeResolver(resolver)
}
func (a *AssistantApp) SetInteractionFoundation(catalog feature.Catalog, sessions *rootinteraction.Runtime, actions *rootinteraction.Dispatcher) {
	a.client.SetInteractionFoundation(catalog, sessions, actions)
}
func (a *AssistantApp) LegacyMenuCompatibility() menu.CompatibilityHost {
	if a.client == nil {
		return nil
	}
	return a.client.LegacyMenuCompatibility()
}
