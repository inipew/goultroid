package assistant

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/assistant/client"
	assistantdeeplink "github.com/inipew/goultroid/internal/assistant/deeplink"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	assistentrpc "github.com/inipew/goultroid/internal/assistant/rpc"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/runtime"
	broadcastsvc "github.com/inipew/goultroid/internal/services/broadcast"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"github.com/inipew/goultroid/internal/services/savedresponse"
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
	SetGroupStateStore(store core.GroupStateStore)
	SetMetricsCollector(m core.MetricsCollector)
	SetCallbackRouter(router client.CoreCallbackDispatcher)
	SetInlineEngine(engine *inline.Engine)
	SetDeepLinkRouter(router *assistantdeeplink.Router)
	SetRelayIngress(relay pmrelay.Ingress)
	SetAudienceRegistry(registry pmrelay.AudienceRegistry)
	SetBroadcastService(service *broadcastsvc.Service)
	BroadcastAudience(context.Context, broadcastsvc.BroadcastRequest) (*broadcastsvc.BroadcastReport, error)
	SetTasks(client tasks.Client)
	SetPluginScopeResolver(resolver func(string) (tasks.ScopeIdentity, bool))
	SetInteractionFoundation(catalog feature.Catalog, sessions *rootinteraction.Runtime, actions *rootinteraction.Dispatcher)
	SetInteractionDrivers(drivers []assistantinteraction.FeatureDriver)
	SetRPCExecutor(executor assistentrpc.Executor)
}

type AssistantApp struct {
	client *client.AssistantClient
	logger *zap.Logger
}

var _ Client = (*AssistantApp)(nil)
var _ runtime.CriticalComponent = (*AssistantApp)(nil)
var _ runtime.Quiescer = (*AssistantApp)(nil)

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
func (a *AssistantApp) Start(ctx context.Context) error     { return a.client.Start(ctx) }
func (a *AssistantApp) Quiesce(ctx context.Context) error   { return a.client.Quiesce(ctx) }
func (a *AssistantApp) Stop(ctx context.Context) error      { return a.client.Stop(ctx) }
func (a *AssistantApp) IsRunning() bool                     { return a.client.IsRunning() }
func (a *AssistantApp) WaitReady(ctx context.Context) error { return a.client.WaitReady(ctx) }
func (a *AssistantApp) Username() string                    { return a.client.Username() }
func (a *AssistantApp) StartTime() time.Time                { return a.client.StartTime() }
func (a *AssistantApp) SetOwner(ownerID int64, sudoGetter func() []int64) {
	a.client.SetOwner(ownerID, sudoGetter)
}
func (a *AssistantApp) SetCoreRouter(router *core.Router) { a.client.SetCoreRouter(router) }
func (a *AssistantApp) SetTasks(client tasks.Client)      { a.client.SetTasks(client) }
func (a *AssistantApp) SetDelayedActions(scheduler core.DelayedActionScheduler) {
	a.client.SetDelayedActions(scheduler)
}
func (a *AssistantApp) SetSettingsService(svc *settings.Service) { a.client.SetSettingsService(svc) }
func (a *AssistantApp) SetGroupStateStore(store core.GroupStateStore) {
	a.client.SetGroupStateStore(store)
}
func (a *AssistantApp) SetSavedResponseBindings(bindings *savedresponse.BindingService, delivery *savedresponse.ResponseDelivery) {
	a.client.SetSavedResponseBindings(bindings, delivery)
}
func (a *AssistantApp) SetMetricsCollector(m core.MetricsCollector) { a.client.SetMetricsCollector(m) }
func (a *AssistantApp) SetCallbackRouter(router client.CoreCallbackDispatcher) {
	a.client.SetCallbackRouter(router)
}
func (a *AssistantApp) SetInlineEngine(engine *inline.Engine) { a.client.SetInlineEngine(engine) }
func (a *AssistantApp) SetDeepLinkRouter(router *assistantdeeplink.Router) {
	a.client.SetDeepLinkRouter(router)
}
func (a *AssistantApp) SetRelayIngress(relay pmrelay.Ingress) {
	a.client.SetRelayIngress(relay)
}
func (a *AssistantApp) SetAudienceRegistry(registry pmrelay.AudienceRegistry) {
	a.client.SetAudienceRegistry(registry)
}
func (a *AssistantApp) SetBroadcastService(service *broadcastsvc.Service) {
	a.client.SetBroadcastService(service)
}
func (a *AssistantApp) BroadcastAudience(
	ctx context.Context,
	req broadcastsvc.BroadcastRequest,
) (*broadcastsvc.BroadcastReport, error) {
	return a.client.BroadcastAudience(ctx, req)
}
func (a *AssistantApp) SetRPCExecutor(executor assistentrpc.Executor) {
	a.client.SetRPCExecutor(executor)
}
func (a *AssistantApp) SetPluginScopeResolver(resolver func(string) (tasks.ScopeIdentity, bool)) {
	a.client.SetPluginScopeResolver(resolver)
}
func (a *AssistantApp) SetInteractionFoundation(catalog feature.Catalog, sessions *rootinteraction.Runtime, actions *rootinteraction.Dispatcher) {
	a.client.SetInteractionFoundation(catalog, sessions, actions)
}
func (a *AssistantApp) SetInteractionDrivers(drivers []assistantinteraction.FeatureDriver) {
	a.client.SetInteractionDrivers(drivers)
}
