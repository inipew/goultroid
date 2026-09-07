package app

import (
	"time"

	"github.com/inipew/goultroid/internal/addon"
	"github.com/inipew/goultroid/internal/assistant"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/scheduler"
	broadcastSvc "github.com/inipew/goultroid/internal/services/broadcast"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/localization"
	mediaSvc "github.com/inipew/goultroid/internal/services/media"
	"github.com/inipew/goultroid/internal/services/moderation"
	pmpermitSvc "github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/internal/services/ratelimit"
	"github.com/inipew/goultroid/internal/services/storage"
	userlogSvc "github.com/inipew/goultroid/internal/services/userlog"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/telegram"
	"go.uber.org/zap"
)

// coreDependencies holds primary storage, routing, permission, and messaging bus components.
type coreDependencies struct {
	db             *database.DB
	perms          *core.Permissions
	router         *core.Router
	eventBus       *core.EventBus
	metrics        core.MetricsCollector
	localizer      localization.Localizer
	callbackStore  *callback.StateStore
	callbackRouter *callback.Router
	inlineEngine   *inline.Engine
	cmdLimiter     *ratelimit.Limiter
	interLimiter   *ratelimit.Limiter
}

// telegramRuntime holds network client, message dispatcher, and optional assistant bot.
type telegramRuntime struct {
	client     *telegram.Client
	dispatcher *telegram.Dispatcher
	assistant  assistant.Client
}

// domainServices holds modular domain-level business capabilities.
type domainServices struct {
	settingsService  *settings.Service
	settingsRegistry *settings.Registry
	modService       *moderation.Service
	schedEngine      *scheduler.Engine
	storage          storage.Storage
	mediaService     *mediaSvc.Service
	downloadRegistry *download.Registry
	pmpermitService  *pmpermitSvc.Service
	broadcastService *broadcastSvc.Service
	userlogService   *userlogSvc.Service
	addonManager     *addon.Manager
	startTime        time.Time
	logger           *zap.Logger
}
