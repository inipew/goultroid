package app

import (
	"context"
	"fmt"
	"time"

	assistantdeeplink "github.com/inipew/goultroid/internal/assistant/deeplink"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/services/storage"
	cloneplugin "github.com/inipew/goultroid/plugins/clone"
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
	providers := make([]database.MigrationProvider, 0, len(builtinModules)+4)
	providers = append(providers,
		assistantdeeplink.MigrationProvider{},
		mediaregistry.MigrationProvider{},
		savedresponse.MigrationProvider{},
	)
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
	// does not prove that durable assets are gone; destructive reconciliation
	// must therefore fail closed. Metadata-only compatibility backfills remain
	// safe and continue independently of physical storage availability.
	durableStore := store != nil && store.BasePath() != "memory://"
	if durableStore {
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
	if _, err := cloneplugin.ReconcileMediaRegistry(
		reconcileCtx,
		db,
		startupPersistentMediaReconcileBatch,
	); err != nil {
		return stats, fmt.Errorf("persistent clone media registry compatibility failed: %w", err)
	}

	// P3-C runs only after every known durable reference source has been
	// migrated/backfilled. Prepared intents come from producers that created
	// bytes but never reached their normal cleanup/disarm point before the prior
	// process stopped. Startup is quiescent, so those guards can become pending
	// immediately. Ephemeral fallback storage never receives delete authority.
	if durableStore {
		reclaimer := mediaregistry.NewReclaimer(mediaregistry.New(db), store)
		if _, err := reclaimer.RecoverClaimsAtStartup(
			reconcileCtx,
			startupPersistentMediaReconcileBatch,
		); err != nil {
			return stats, fmt.Errorf("recover global media reclamation claims failed: %w", err)
		}
		if _, err := reclaimer.DiscoverReclamations(
			reconcileCtx,
			[]mediaregistry.ReclamationPolicy{
				{
					Owner:      "clone",
					Lifecycle:  mediaregistry.LifecyclePersistent,
					MinimumAge: 0,
					Reason:     "startup clone snapshot orphan policy",
				},
				{
					Owner:      "media",
					Lifecycle:  mediaregistry.LifecycleTransient,
					MinimumAge: 0,
					Reason:     "startup transient media orphan policy",
				},
			},
			startupPersistentMediaReconcileBatch,
		); err != nil {
			return stats, fmt.Errorf("discover global media reclamation candidates failed: %w", err)
		}
		if _, _, err := reclaimer.ActivatePreparedAtStartup(
			reconcileCtx,
			startupPersistentMediaReconcileBatch,
		); err != nil {
			return stats, fmt.Errorf("activate prepared global media reclamation failed: %w", err)
		}
		if _, err := reclaimer.Reconcile(reconcileCtx, startupPersistentMediaReconcileBatch); err != nil {
			return stats, fmt.Errorf("global media reclamation failed: %w", err)
		}
	}
	return stats, nil
}
