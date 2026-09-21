package mediaregistry

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/inipew/goultroid/internal/services/storage"
)

type blockingDeleteStorage struct {
	storage.Storage
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingDeleteStorage) Delete(ctx context.Context, id string) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
		return s.Storage.Delete(ctx, id)
	}
}

func TestDeletingClaimBlocksLastMomentReReference(t *testing.T) {
	base := storage.NewMemoryStorage()
	store := &blockingDeleteStorage{Storage: base, started: make(chan struct{}), release: make(chan struct{})}
	registry, reclaimer, _ := newReclaimerTest(t, store)
	asset := putReclaimerAsset(t, base, registry, "test", LifecyclePersistent)
	if err := reclaimer.RequestReclamation(context.Background(), ReclamationRequest{
		AssetID: asset.ID, Owner: "test", Lifecycle: LifecyclePersistent,
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := reclaimer.Reconcile(context.Background(), 1)
		done <- err
	}()
	<-store.started

	err := registry.UpsertReference(context.Background(), Reference{
		AssetID: asset.ID, Subsystem: "test", Kind: "row", Key: "late",
	})
	if !errors.Is(err, ErrReclamationInProgress) {
		t.Fatalf("late reference error=%v, want ErrReclamationInProgress", err)
	}
	close(store.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := base.Stat(context.Background(), asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("claimed asset survived delete: %v", err)
	}
}

func TestCancellationReleasesClaimForDurableRetry(t *testing.T) {
	base := storage.NewMemoryStorage()
	store := &blockingDeleteStorage{Storage: base, started: make(chan struct{}), release: make(chan struct{})}
	registry, reclaimer, _ := newReclaimerTest(t, store)
	asset := putReclaimerAsset(t, base, registry, "test", LifecycleTransient)
	if err := reclaimer.RequestReclamation(context.Background(), ReclamationRequest{
		AssetID: asset.ID, Owner: "test", Lifecycle: LifecycleTransient,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := reclaimer.Reconcile(ctx, 1)
		done <- err
	}()
	<-store.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("reconcile cancellation error=%v, want context.Canceled", err)
	}
	intent, err := reclaimer.Intent(context.Background(), asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.State != ReclamationPending || intent.Attempts != 1 || intent.ClaimToken != "" || intent.ClaimedAt != nil {
		t.Fatalf("cancelled delete left unrecoverable claim: %+v", intent)
	}
	if _, err := base.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("cancelled delete removed asset unexpectedly: %v", err)
	}
}
