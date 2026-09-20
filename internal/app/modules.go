package app

import (
	"context"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/services/storage"
)

//go:generate go run ../../tools/featuregen

func registerBuiltinModules(ctx context.Context, rt *module.Runtime) error {
	ordered, err := module.ResolveOrder(builtinModules)
	if err != nil {
		return fmt.Errorf("resolve module dependencies: %w", err)
	}
	for _, m := range ordered {
		if err := m.Register(ctx, rt); err != nil {
			return fmt.Errorf("failed to register feature module %q: %w", m.Manifest().ID, err)
		}
	}
	return nil
}

func migrateBuiltinFeatures(ctx context.Context, db *database.DB) error {
	providers := make([]database.MigrationProvider, 0, len(builtinModules)+1)
	providers = append(providers, mediaregistry.MigrationProvider{})
	for _, m := range builtinModules {
		provider, ok := m.(database.MigrationProvider)
		if !ok {
			continue
		}
		providers = append(providers, provider)
	}
	if err := database.RunFeatureMigrations(ctx, db, providers...); err != nil {
		return fmt.Errorf("feature migrations failed: %w", err)
	}
	return nil
}

const (
	startupPersistentMediaReconcileBatch   = 64
	startupPersistentMediaReconcileTimeout = 10 * time.Second
)

func reconcileBuiltinPersistentMedia(
	ctx context.Context,
	db *database.DB,
	store storage.Storage,
) (savedresponse.PersistentMediaReconcileStats, error) {
	var stats savedresponse.PersistentMediaReconcileStats
	if db == nil {
		return stats, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reconcileCtx, cancel := context.WithTimeout(ctx, startupPersistentMediaReconcileTimeout)
	defer cancel()

	// FileStorage is the durable source of truth. If startup fell back to the
	// process-local memory backend (or storage is unavailable), an empty store
	// does not prove that durable assets are gone; destructive P3-A
	// reconciliation must therefore fail closed. The ledger-only backfill is
	// metadata-only and remains safe, so keep migration progress independent of
	// physical storage availability.
	if store != nil && store.BasePath() != "memory://" {
		var err error
		stats, err = savedresponse.NewService(store, db).ReconcilePersistentMedia(
			reconcileCtx,
			startupPersistentMediaReconcileBatch,
		)
		if err != nil {
			return stats, fmt.Errorf("persistent saved-response media reconciliation failed: %w", err)
		}
	} else {
		if _, err := savedresponse.BackfillPersistentMediaLedger(
			reconcileCtx,
			db,
			startupPersistentMediaReconcileBatch,
		); err != nil {
			return stats, fmt.Errorf("persistent saved-response media ledger backfill failed: %w", err)
		}
	}

	if _, err := savedresponse.ReconcileRegistryCompatibility(
		reconcileCtx,
		db,
		startupPersistentMediaReconcileBatch,
	); err != nil {
		return stats, fmt.Errorf("persistent saved-response media registry compatibility failed: %w", err)
	}
	return stats, nil
}
