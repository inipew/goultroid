package app

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/inipew/goultroid/internal/addon"
	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/scheduler"
	broadcastSvc "github.com/inipew/goultroid/internal/services/broadcast"
	"github.com/inipew/goultroid/internal/services/download"
	mediaSvc "github.com/inipew/goultroid/internal/services/media"
	pmpermitSvc "github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/services/storage"
	userlogSvc "github.com/inipew/goultroid/internal/services/userlog"
	"github.com/inipew/goultroid/internal/settings"
	pmpermitPlugin "github.com/inipew/goultroid/plugins/pmpermit"
	"go.uber.org/zap"
)

func buildDomainServices(cfg *config.Config, core *coreDependencies, tg *telegramRuntime, logger *zap.Logger) (*domainServices, error) {
	startTime := time.Now()

	settingsRegistry := settings.NewRegistry()
	if err := settings.RegisterDefaultDefinitions(settingsRegistry); err != nil {
		cleanupCore(core, logger)
		return nil, fmt.Errorf("register default settings definitions: %w", err)
	}
	settingsRepo := settings.NewSQLiteRepository(core.db.DB)
	settingsService := settings.NewService(settingsRepo, settingsRegistry, core.eventBus)

	schedRepo := scheduler.NewSQLiteRepository(core.db.DB)
	schedEngine := scheduler.NewEngine(schedRepo, tg.client.Service, core.router, core.perms, logger)
	schedEngine.SetExecutor(tg.dispatcher.Executor())
	if core.workerManager != nil {
		schedEngine.SetWorkers(core.workerManager, core.taskManager)
	}
	if core.jobsManager != nil {
		schedEngine.SetJobsManager(core.jobsManager)
	}

	storageDir := filepath.Join("data", "storage")
	fileStorage, err := storage.NewFileStorage(storageDir, 10*1024*1024*1024)
	var appStorage storage.Storage = fileStorage
	if err != nil {
		logger.Warn("failed to initialize persistent file storage, falling back to memory", zap.Error(err))
		appStorage = storage.NewMemoryStorage()
	}

	processRunner := process.NewOSRunner(3, 5*time.Minute, 4*1024*1024)
	downloadRegistry := download.NewRegistry(
		download.NewExtractorProvider(processRunner, 500*1024*1024),
		download.NewDirectHTTPProvider(5*time.Minute, 500*1024*1024),
	)
	mediaGuard := mediaSvc.NewResourceGuard(2, 100*1024*1024)
	mediaService := mediaSvc.NewService(processRunner, appStorage, mediaGuard)

	pmpermitRepo := pmpermitPlugin.NewSQLiteRepository(core.db)
	pmpermitService := pmpermitSvc.NewService(pmpermitRepo, tg.client.Service, cfg.OwnerID, core.perms, logger)
	pmpermitService.SetEventBus(core.eventBus)

	broadcastService := broadcastSvc.NewService(tg.client.Service, logger)
	userlogRepo := userlogSvc.NewSQLiteRepository(core.db)
	userlogService := userlogSvc.NewService(userlogRepo, tg.client.Service, logger)

	addonGate := addon.NewCapabilityGate()
	addonRepo := addon.NewSQLiteRepository(core.db)
	addonManager := addon.NewManager(addonRepo, addonGate, "1.0.0", logger)
	addonCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = addonManager.LoadInstalled(addonCtx)
	cancel()
	if err != nil {
		logger.Warn("failed to load installed addons", zap.Error(err))
	}

	return &domainServices{
		settingsService:  settingsService,
		settingsRegistry: settingsRegistry,
		schedEngine:      schedEngine,
		storage:          appStorage,
		mediaService:     mediaService,
		processRunner:    processRunner,
		downloadRegistry: downloadRegistry,
		pmpermitService:  pmpermitService,
		broadcastService: broadcastService,
		userlogService:   userlogService,
		addonManager:     addonManager,
		startTime:        startTime,
		logger:           logger,
	}, nil
}
