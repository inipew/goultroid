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

// CoreRuntime contains application-wide infrastructure that modules may need
// during registration. It intentionally contains no feature-specific state.
type CoreRuntime struct {
	DB          *database.DB
	Permissions *core.Permissions
	Plugins     *plugin.Manager
	Router      *core.Router
	EventBus    *core.EventBus
	Metrics     core.MetricsCollector
}

// TelegramRuntime contains Telegram-facing capabilities shared by modules.
type TelegramRuntime struct {
	TelegramService func() core.TelegramServicer
	Resolver        core.PeerResolver
	Callbacks       *callback.Router
	CallbackStore   *callback.StateStore
}

// ServiceRuntime contains reusable cross-feature services. Feature-owned
// persistence must not be added here; it belongs to the feature package.
type ServiceRuntime struct {
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

// Runtime is the composition context supplied to feature modules.
//
// The embedded capability groups are a transitional boundary: existing modules
// may continue to use promoted fields (rt.DB, rt.Resolver, ...), while new code
// should depend on the narrowest capability group it actually needs. This keeps
// application composition explicit without turning Runtime into a service
// locator or map[string]any.
type Runtime struct {
	OwnerID   int64
	Logger    *zap.Logger
	StartTime time.Time

	CoreRuntime
	TelegramRuntime
	ServiceRuntime
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
