package module

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/addon"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/scheduler"
	broadcastSvc "github.com/inipew/goultroid/internal/services/broadcast"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/download"
	mediaSvc "github.com/inipew/goultroid/internal/services/media"
	"github.com/inipew/goultroid/internal/services/moderation"
	pmpermitSvc "github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/internal/services/storage"
	userlogSvc "github.com/inipew/goultroid/internal/services/userlog"
	"github.com/inipew/goultroid/internal/settings"
	"go.uber.org/zap"
)

type Manifest struct {
	ID           string
	Version      string
	Description  string
	Dependencies []string
}

type Runtime struct {
	DB               *database.DB
	OwnerID          int64
	Permissions      *core.Permissions
	Plugins          *plugin.Manager
	Router           *core.Router
	EventBus         *core.EventBus
	Metrics          core.MetricsCollector
	Logger           *zap.Logger
	StartTime        time.Time
	TelegramService  func() core.TelegramServicer
	Resolver         core.PeerResolver
	Callbacks        *callback.Router
	CallbackStore    *callback.StateStore
	Storage          storage.Storage
	DownloadRegistry *download.Registry
	MediaService     *mediaSvc.Service
	ModService       *moderation.Service
	PMPermitService  *pmpermitSvc.Service
	BroadcastService *broadcastSvc.Service
	UserlogService   *userlogSvc.Service
	AddonManager     *addon.Manager
	SettingsService  *settings.Service
	SchedEngine      *scheduler.Engine
}

type Module interface {
	Manifest() Manifest
	Register(context.Context, *Runtime) error
}

func Validate(m Module) error {
	if m == nil {
		return ErrNilModule
	}
	manifest := m.Manifest()
	if manifest.ID == "" {
		return ErrEmptyModuleID
	}
	if manifest.Version == "" {
		return ErrEmptyModuleVersion
	}
	return nil
}
