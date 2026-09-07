package assistant

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/client"
	"go.uber.org/zap"
)

// Client defines the lifecycle and metadata methods for the assistant bot client.
type Client interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	IsRunning() bool
	Username() string
	StartTime() time.Time
}

// AssistantApp serves as the top-level application facade for the Assistant subsystem.
type AssistantApp struct {
	client *client.AssistantClient
	logger *zap.Logger
}

var _ Client = (*AssistantApp)(nil)

// NewApp creates a configured AssistantApp instance.
func NewApp(appID int, appHash string, botToken string, logger *zap.Logger) *AssistantApp {
	if logger == nil {
		logger = zap.NewNop()
	}
	cli := client.NewAssistantClient(appID, appHash, botToken, logger)
	return &AssistantApp{
		client: cli,
		logger: logger,
	}
}

// NewBotClient provides backward-compatibility alias for NewApp.
func NewBotClient(appID int, appHash string, botToken string, logger *zap.Logger) *AssistantApp {
	return NewApp(appID, appHash, botToken, logger)
}

// Start launches the assistant client.
func (a *AssistantApp) Start(ctx context.Context) error {
	return a.client.Start(ctx)
}

// Stop gracefully shuts down the assistant client.
func (a *AssistantApp) Stop(ctx context.Context) error {
	return a.client.Stop(ctx)
}

// IsRunning reports whether the assistant is actively running.
func (a *AssistantApp) IsRunning() bool {
	return a.client.IsRunning()
}

// Username returns the authenticated bot username.
func (a *AssistantApp) Username() string {
	return a.client.Username()
}

// StartTime returns when the assistant was initialized.
func (a *AssistantApp) StartTime() time.Time {
	return a.client.StartTime()
}

// SetAuthorizer configures callback authorization.
func (a *AssistantApp) SetAuthorizer(auth callback.Authorizer) {
	a.client.SetAuthorizer(auth)
}

// SetOwner restricts actions to the specified bot owner and optional sudo users.
func (a *AssistantApp) SetOwner(ownerID int64, sudoGetter func() []int64) {
	a.client.SetAuthorizer(callback.NewOwnerAuthorizer(ownerID, sudoGetter))
}
