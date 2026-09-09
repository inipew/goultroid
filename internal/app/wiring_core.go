package app

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/idempotency"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/platform/audit"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/platform/process"
	"github.com/inipew/goultroid/internal/platform/secret"
	platformStorage "github.com/inipew/goultroid/internal/platform/storage"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/localization"
	"github.com/inipew/goultroid/internal/services/ratelimit"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
	"github.com/inipew/goultroid/plugins/sudo"
	"go.uber.org/zap"
)

func buildCore(cfg *config.Config, logger *zap.Logger) (*coreDependencies, error) {
	dbPath := cfg.DatabasePath
	if dbPath == "" {
		dbPath = "data/goultroid.db"
	}
	db, err := database.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("initialize database: %w", err)
	}

	perms := core.NewPermissions(cfg.OwnerID, cfg.SudoUsers)
	sudoCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	sudoRepo := sudo.NewSQLiteRepository(db)
	dbSudos, err := sudoRepo.GetSudoUsers(sudoCtx)
	cancel()
	if err != nil {
		logger.Warn("failed to load sudo users from db", zap.Error(err))
	} else {
		for _, u := range dbSudos {
			perms.AddSudo(u.UserID)
		}
		logger.Info("loaded dynamic sudo users", zap.Int("count", len(dbSudos)))
	}

	router := core.NewRouter(cfg.Prefix)
	eventBus := core.NewEventBus()
	metrics := core.NewDefaultMetricsTracker()
	localizer := localization.New("id")

	cmdLimiter := ratelimit.New(ratelimit.Policy{Limit: 60, Window: time.Minute, Burst: 30}, 5*time.Minute)
	interLimiter := ratelimit.New(ratelimit.Policy{Limit: 30, Window: time.Minute, Burst: 10}, 5*time.Minute)

	callbackStore := callback.NewStateStore()
	callbackRouter := callback.NewRouter(logger, callbackStore)
	callbackRouter.SetMetrics(metrics)
	callbackRouter.SetLimiter(interLimiter)
	callbackRouter.SetTimeout(15 * time.Second)

	inlineRegistry := inline.NewRegistry()
	if err := inlineRegistry.Register(&defaultCatchAllInlineHandler{router: router, startTime: time.Now()}); err != nil {
		cleanupCore(&coreDependencies{db: db, eventBus: eventBus}, logger)
		return nil, fmt.Errorf("register catch-all inline handler: %w", err)
	}
	if err := inlineRegistry.Register(&defaultHelpInlineHandler{router: router}); err != nil {
		cleanupCore(&coreDependencies{db: db, eventBus: eventBus}, logger)
		return nil, fmt.Errorf("register help inline handler: %w", err)
	}
	if err := inlineRegistry.Register(&defaultPingInlineHandler{startTime: time.Now()}); err != nil {
		cleanupCore(&coreDependencies{db: db, eventBus: eventBus}, logger)
		return nil, fmt.Errorf("register ping inline handler: %w", err)
	}

	inlineEngine := inline.NewEngine(inlineRegistry, logger)
	inlineEngine.SetMetrics(metrics)
	inlineEngine.SetLimiter(interLimiter)
	inlineEngine.SetTimeout(4 * time.Second)
	inlineEngine.SetPermissions(perms)

	workerManager := workers.NewManager()
	taskManager := tasks.NewManager()
	workerManager.SetTasksManager(taskManager)
	resourceManager := resource.NewManager()
	idempManager := idempotency.NewManager(1 * time.Minute)

	jobsRepo := jobs.NewSQLiteRepository(db.DB)
	if err := jobsRepo.InitSchema(context.Background()); err != nil {
		logger.Warn("failed to initialize managed jobs schema", zap.Error(err))
	}
	jobsManager := jobs.NewManager(workerManager, jobsRepo)
	jobsManager.SetIdempotencyManager(idempManager)

	dataDir := "data"
	if cfg.DatabasePath != "" {
		dataDir = filepath.Dir(cfg.DatabasePath)
	}
	fsManager, err := filesystem.NewManager(dataDir, "", "", resourceManager)
	if err != nil {
		logger.Warn("failed to create filesystem manager, using fallback", zap.Error(err))
		fsManager, _ = filesystem.NewManager("data", "", "", resourceManager)
	}
	procManager := process.NewManager([]string{"ffmpeg", "ffprobe", "yt-dlp", "tesseract", "git", "sh", "bash", "*"}, 10*1024*1024, resourceManager)
	netService := network.NewService(nil, resourceManager)
	auditService := audit.NewService(logger.Named("audit"), 1000)
	procManager.SetAuditor(auditService)
	secretManager := secret.NewManager(map[string]string{
		"APP_ID":    fmt.Sprintf("%d", cfg.AppID),
		"APP_HASH":  cfg.AppHash,
		"BOT_TOKEN": cfg.BotToken,
	})
	secretManager.SetAuditor(auditService)
	storageManager := platformStorage.NewManager(db.DB)
	if err := storageManager.InitSchema(context.Background()); err != nil {
		logger.Warn("failed to initialize plugin storage schema", zap.Error(err))
	}
	storageManager.SetAuditor(auditService)
	capGate := plugin.NewCapabilityGate()
	capGate.SetAuditor(auditService)
	capGate.SetFailClosed(true)
	capGate.AllowPrivileged("system", plugin.CapProcessExecute)
	capGate.AllowPrivileged("media", plugin.CapProcessExecute)
	capGate.AllowPrivileged("downloader", plugin.CapProcessExecute)
	capGate.AllowPrivileged("voice", plugin.CapProcessExecute)
	capGate.AllowPrivileged("sticker", plugin.CapProcessExecute)
	capGate.AllowPrivileged("addon", plugin.CapProcessExecute)
	capGate.AllowPrivileged("ocr", plugin.CapSecretRead)

	return &coreDependencies{
		db:              db,
		perms:           perms,
		router:          router,
		eventBus:        eventBus,
		metrics:         metrics,
		localizer:       localizer,
		callbackStore:   callbackStore,
		callbackRouter:  callbackRouter,
		inlineEngine:    inlineEngine,
		cmdLimiter:      cmdLimiter,
		interLimiter:    interLimiter,
		workerManager:   workerManager,
		taskManager:     taskManager,
		jobsManager:     jobsManager,
		resourceManager: resourceManager,
		idempManager:    idempManager,
		fsManager:       fsManager,
		procManager:     procManager,
		netService:      netService,
		secretManager:   secretManager,
		storageManager:  storageManager,
		auditService:    auditService,
		capGate:         capGate,
	}, nil
}
