package module

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/addon"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/audit"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/platform/process"
	"github.com/inipew/goultroid/internal/platform/secret"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/scheduler"
	broadcastSvc "github.com/inipew/goultroid/internal/services/broadcast"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/download"
	mediaSvc "github.com/inipew/goultroid/internal/services/media"
	pmpermitSvc "github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/internal/services/storage"
	userlogSvc "github.com/inipew/goultroid/internal/services/userlog"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
	"go.uber.org/zap"
)

// Manifest is the unified declaration of identity, capabilities, dependencies, and metadata.
type Manifest = plugin.Manifest

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
// persistence and feature-specific business services must not be added here.
type ServiceRuntime struct {
	Storage          storage.Storage
	DownloadRegistry *download.Registry
	MediaService     *mediaSvc.Service
	PMPermitService  *pmpermitSvc.Service
	BroadcastService *broadcastSvc.Service
	UserlogService   *userlogSvc.Service
	AddonManager     *addon.Manager
	SettingsService  *settings.Service
	SchedEngine      *scheduler.Engine
}

// PlatformRuntime contains capability-gated platform accessors.
type PlatformRuntime struct {
	Gate      *plugin.CapabilityGate
	Network   *network.Service
	Process   *process.Manager
	Files     *filesystem.Manager
	Secrets   *secret.Manager
	Audit     *audit.Service
	Resources *resource.Manager
	Workers   *workers.Manager
	Tasks     *tasks.Manager
	Jobs      *jobs.Manager
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
	PlatformRuntime
}

// RegisterPlugin registers a plugin under the module's manifest with capability gating.
func (rt *Runtime) RegisterPlugin(ctx context.Context, manifest Manifest, p plugin.Plugin) error {
	if rt == nil {
		return ErrNilRuntime
	}
	if rt.Plugins == nil {
		return ErrNilPluginManager
	}
	return rt.Plugins.RegisterModule(ctx, manifest, p)
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
