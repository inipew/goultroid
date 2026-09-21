package mediaregistry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/storage"
)

func newReclaimerTest(t *testing.T, store storage.Storage) (*Registry, *Reclaimer, *database.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	registry := New(db)
	return registry, NewReclaimer(registry, store), db
}

func putReclaimerAsset(t *testing.T, store storage.Storage, registry *Registry, owner string, lifecycle Lifecycle) *storage.Asset {
	t.Helper()
	asset, err := store.Put(context.Background(), strings.NewReader("payload"), storage.Metadata{Name: "payload.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterAsset(context.Background(), AssetRegistration{
		AssetID: asset.ID, Producer: owner + ".test", Owner: owner, Lifecycle: lifecycle,
	}); err != nil {
		t.Fatal(err)
	}
	return asset
}

func TestReclaimerDoesNotInferAuthorizationFromZeroReferences(t *testing.T) {
	store := storage.NewMemoryStorage()
	registry, reclaimer, _ := newReclaimerTest(t, store)
	asset := putReclaimerAsset(t, store, registry, "test", LifecyclePersistent)

	stats, err := reclaimer.Reconcile(context.Background(), 16)
	if err != nil {
		t.Fatal(err)
	}
	if stats != (ReclamationStats{}) {
		t.Fatalf("unexpected reclamation without explicit intent: %+v", stats)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("unreferenced asset was deleted without intent: %v", err)
	}
}

func TestReclamationGraceDefersPhysicalDelete(t *testing.T) {
	store := storage.NewMemoryStorage()
	registry, reclaimer, _ := newReclaimerTest(t, store)
	asset := putReclaimerAsset(t, store, registry, "test", LifecyclePersistent)

	if err := reclaimer.RequestReclamation(context.Background(), ReclamationRequest{
		AssetID: asset.ID, Owner: "test", Lifecycle: LifecyclePersistent, Reason: "grace", Grace: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := reclaimer.Reconcile(context.Background(), 16)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Scanned != 0 {
		t.Fatalf("grace-period intent became due: %+v", stats)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("asset disappeared during grace period: %v", err)
	}
}

func TestDurableReferenceCancelsPreparedReclamation(t *testing.T) {
	store := storage.NewMemoryStorage()
	registry, reclaimer, _ := newReclaimerTest(t, store)
	asset := putReclaimerAsset(t, store, registry, "test", LifecyclePersistent)

	if err := reclaimer.PrepareReclamation(context.Background(), ReclamationRequest{
		AssetID: asset.ID, Owner: "test", Lifecycle: LifecyclePersistent, Reason: "prepare", Grace: DefaultReclamationGrace,
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.UpsertReference(context.Background(), Reference{
		AssetID: asset.ID, Subsystem: "test", Kind: "row", Key: "1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reclaimer.Intent(context.Background(), asset.ID); !errors.Is(err, ErrReclamationIntentGone) {
		t.Fatalf("reference did not cancel prepared intent: %v", err)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("re-referenced asset disappeared: %v", err)
	}
}

func TestLegacyLifecycleCannotAuthorizeGlobalReclamation(t *testing.T) {
	store := storage.NewMemoryStorage()
	registry, reclaimer, _ := newReclaimerTest(t, store)
	asset := putReclaimerAsset(t, store, registry, "legacy-owner", LifecycleLegacy)

	err := reclaimer.RequestReclamation(context.Background(), ReclamationRequest{
		AssetID: asset.ID, Owner: "legacy-owner", Lifecycle: LifecycleLegacy,
	})
	if !errors.Is(err, ErrReclamationNotAllowed) {
		t.Fatalf("legacy reclamation error=%v, want ErrReclamationNotAllowed", err)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("legacy asset was touched: %v", err)
	}
}

func TestRetainedLifecycleCannotUsePreparedCrashGuard(t *testing.T) {
	store := storage.NewMemoryStorage()
	registry, reclaimer, _ := newReclaimerTest(t, store)
	asset := putReclaimerAsset(t, store, registry, "downloader", LifecycleRetained)

	err := reclaimer.PrepareReclamation(context.Background(), ReclamationRequest{
		AssetID: asset.ID, Owner: "downloader", Lifecycle: LifecycleRetained,
	})
	if !errors.Is(err, ErrReclamationNotAllowed) {
		t.Fatalf("retained prepare error=%v, want ErrReclamationNotAllowed", err)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("retained asset was touched: %v", err)
	}
}

func TestErrNotFoundCompletesReclamationIdempotently(t *testing.T) {
	store := storage.NewMemoryStorage()
	registry, reclaimer, _ := newReclaimerTest(t, store)
	if err := registry.RegisterAsset(context.Background(), AssetRegistration{
		AssetID: "missing-physical", Producer: "test", Owner: "test", Lifecycle: LifecycleTransient,
	}); err != nil {
		t.Fatal(err)
	}

	if err := reclaimer.ReclaimNow(context.Background(), ReclamationRequest{
		AssetID: "missing-physical", Owner: "test", Lifecycle: LifecycleTransient, Reason: "already missing",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Asset(context.Background(), "missing-physical"); !errors.Is(err, ErrAssetNotRegistered) {
		t.Fatalf("metadata survived ErrNotFound completion: %v", err)
	}
	if _, err := reclaimer.Intent(context.Background(), "missing-physical"); !errors.Is(err, ErrReclamationIntentGone) {
		t.Fatalf("intent survived ErrNotFound completion: %v", err)
	}
}

type failingDeleteStorage struct {
	storage.Storage
	err error
}

func (s *failingDeleteStorage) Delete(context.Context, string) error { return s.err }

func TestDeleteFailurePersistsBoundedBackoff(t *testing.T) {
	base := storage.NewMemoryStorage()
	store := &failingDeleteStorage{Storage: base, err: errors.New("disk busy")}
	registry, reclaimer, _ := newReclaimerTest(t, store)
	asset := putReclaimerAsset(t, base, registry, "test", LifecycleTransient)
	if err := reclaimer.RequestReclamation(context.Background(), ReclamationRequest{
		AssetID: asset.ID, Owner: "test", Lifecycle: LifecycleTransient,
	}); err != nil {
		t.Fatal(err)
	}

	before := time.Now().UTC()
	stats, err := reclaimer.Reconcile(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deferred != 1 || stats.Claimed != 1 {
		t.Fatalf("unexpected failure stats: %+v", stats)
	}
	intent, err := reclaimer.Intent(context.Background(), asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.State != ReclamationPending || intent.Attempts != 1 || !intent.NextAttemptAt.After(before) {
		t.Fatalf("failure did not persist backoff: %+v", intent)
	}
	if got := reclamationRetryDelay(100); got > time.Hour {
		t.Fatalf("retry delay exceeded cap: %v", got)
	}
}

func TestReclamationBatchIsBounded(t *testing.T) {
	store := storage.NewMemoryStorage()
	registry, reclaimer, _ := newReclaimerTest(t, store)
	for i := 0; i < 3; i++ {
		asset := putReclaimerAsset(t, store, registry, "test", LifecycleTransient)
		if err := reclaimer.RequestReclamation(context.Background(), ReclamationRequest{
			AssetID: asset.ID, Owner: "test", Lifecycle: LifecycleTransient,
		}); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := reclaimer.Reconcile(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Scanned != 1 || stats.Deleted != 1 {
		t.Fatalf("bounded pass stats=%+v, want one deletion", stats)
	}
	page, err := store.Enumerate(context.Background(), storage.EnumerationOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("remaining physical assets=%d, want 2", len(page.Entries))
	}
}

func TestStartupActivationRecoversPreparedIntent(t *testing.T) {
	store := storage.NewMemoryStorage()
	registry, reclaimer, _ := newReclaimerTest(t, store)
	asset := putReclaimerAsset(t, store, registry, "test", LifecycleTransient)
	if err := reclaimer.PrepareReclamation(context.Background(), ReclamationRequest{
		AssetID: asset.ID, Owner: "test", Lifecycle: LifecycleTransient, Grace: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}

	activated, cancelled, err := reclaimer.ActivatePreparedAtStartup(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if activated != 1 || cancelled != 0 {
		t.Fatalf("startup activation=(%d,%d), want (1,0)", activated, cancelled)
	}
	stats, err := reclaimer.Reconcile(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deleted != 1 {
		t.Fatalf("startup recovery stats=%+v, want one delete", stats)
	}
}

func TestOwnerStorageFailsClosedForUnregisteredPhysicalAsset(t *testing.T) {
	base := storage.NewMemoryStorage()
	registry, _, _ := newReclaimerTest(t, base)
	asset, err := base.Put(context.Background(), strings.NewReader("legacy"), storage.Metadata{Name: "legacy.bin"})
	if err != nil {
		t.Fatal(err)
	}
	ownerStore := NewOwnerStorage(base, registry, "test.producer", "test", LifecyclePersistent, "test cleanup")
	if err := ownerStore.Delete(context.Background(), asset.ID); !errors.Is(err, ErrAssetNotRegistered) {
		t.Fatalf("unregistered delete error=%v, want ErrAssetNotRegistered", err)
	}
	if _, err := base.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("unregistered physical asset was touched: %v", err)
	}
}

func TestOwnerStoragePutRegistersPreparedGuardAndDeleteUsesReclaimer(t *testing.T) {
	base := storage.NewMemoryStorage()
	registry, _, _ := newReclaimerTest(t, base)
	ownerStore := NewOwnerStorage(base, registry, "clone.snapshot", "clone", LifecyclePersistent, "clone cleanup")

	asset, err := ownerStore.Put(context.Background(), strings.NewReader("snapshot"), storage.Metadata{Name: "snapshot.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := registry.Asset(context.Background(), asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Producer != "clone.snapshot" || record.Owner != "clone" || record.Lifecycle != LifecyclePersistent {
		t.Fatalf("unexpected owner storage registration: %+v", record)
	}
	intent, err := NewReclaimer(registry, base).Intent(context.Background(), asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.State != ReclamationPrepared {
		t.Fatalf("owner storage guard state=%q, want prepared", intent.State)
	}

	if err := ownerStore.Delete(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Stat(context.Background(), asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("owner storage delete left physical asset: %v", err)
	}
	if _, err := registry.Asset(context.Background(), asset.ID); !errors.Is(err, ErrAssetNotRegistered) {
		t.Fatalf("owner storage delete left metadata: %v", err)
	}
}

func TestDiscoveryRequiresExplicitLifecyclePolicy(t *testing.T) {
	store := storage.NewMemoryStorage()
	registry, reclaimer, _ := newReclaimerTest(t, store)
	transient := putReclaimerAsset(t, store, registry, "media", LifecycleTransient)
	retained := putReclaimerAsset(t, store, registry, "downloader", LifecycleRetained)

	discovered, err := reclaimer.DiscoverReclamations(context.Background(), []ReclamationPolicy{
		{Owner: "media", Lifecycle: LifecycleTransient, Reason: "transient policy"},
	}, 16)
	if err != nil {
		t.Fatal(err)
	}
	if discovered != 1 {
		t.Fatalf("discovered=%d, want 1", discovered)
	}
	stats, err := reclaimer.Reconcile(context.Background(), 16)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deleted != 1 {
		t.Fatalf("policy reclamation stats=%+v, want one delete", stats)
	}
	if _, err := store.Stat(context.Background(), transient.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("policy-authorized transient survived: %v", err)
	}
	if _, err := store.Stat(context.Background(), retained.ID); err != nil {
		t.Fatalf("retained asset was inferred reclaimable from zero refs: %v", err)
	}

	if _, err := reclaimer.DiscoverReclamations(context.Background(), []ReclamationPolicy{
		{Owner: "downloader", Lifecycle: LifecycleRetained},
	}, 16); !errors.Is(err, ErrReclamationNotAllowed) {
		t.Fatalf("retained discovery policy error=%v, want ErrReclamationNotAllowed", err)
	}
}

func TestReclaimNowReportsPersistedBackoffInsteadOfFalseSuccess(t *testing.T) {
	base := storage.NewMemoryStorage()
	store := &failingDeleteStorage{Storage: base, err: errors.New("disk busy")}
	registry, reclaimer, _ := newReclaimerTest(t, store)
	asset := putReclaimerAsset(t, base, registry, "test", LifecycleTransient)

	err := reclaimer.ReclaimNow(context.Background(), ReclamationRequest{
		AssetID: asset.ID, Owner: "test", Lifecycle: LifecycleTransient,
	})
	if err == nil || errors.Is(err, ErrReclamationInProgress) {
		t.Fatalf("initial delete error=%v, want physical storage failure", err)
	}

	intent, err := reclaimer.Intent(context.Background(), asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.State != ReclamationPending || intent.Attempts != 1 || !intent.NextAttemptAt.After(time.Now().UTC()) {
		t.Fatalf("initial failure did not persist backoff: %+v", intent)
	}

	err = reclaimer.ReclaimNow(context.Background(), ReclamationRequest{
		AssetID: asset.ID, Owner: "test", Lifecycle: LifecycleTransient,
	})
	if !errors.Is(err, ErrReclamationInProgress) {
		t.Fatalf("backoff retry error=%v, want ErrReclamationInProgress", err)
	}
	if _, statErr := base.Stat(context.Background(), asset.ID); statErr != nil {
		t.Fatalf("deferred asset disappeared: %v", statErr)
	}
}
