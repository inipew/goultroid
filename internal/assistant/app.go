package assistant

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/client"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/settings"
	"go.uber.org/zap"
)

type Client interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	IsRunning() bool
	Username() string
	StartTime() time.Time
	SetCoreRouter(router *core.Router)
	SetOwner(ownerID int64, sudoGetter func() []int64)
	SetSettingsService(svc *settings.Service)
	SetMetricsCollector(m core.MetricsCollector)
}

type AssistantApp struct {
	client *client.AssistantClient
	logger *zap.Logger
}

var _ Client = (*AssistantApp)(nil)

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
func (a *AssistantApp) Username() string                       { return a.client.Username() }
func (a *AssistantApp) StartTime() time.Time                   { return a.client.StartTime() }
func (a *AssistantApp) SetAuthorizer(auth callback.Authorizer) { a.client.SetAuthorizer(auth) }
func (a *AssistantApp) SetOwner(ownerID int64, sudoGetter func() []int64) {
	a.client.SetOwner(ownerID, sudoGetter)
}
func (a *AssistantApp) SetCoreRouter(router *core.Router)           { a.client.SetCoreRouter(router) }
func (a *AssistantApp) SetSettingsService(svc *settings.Service)    { a.client.SetSettingsService(svc) }
func (a *AssistantApp) SetMetricsCollector(m core.MetricsCollector) { a.client.SetMetricsCollector(m) }
