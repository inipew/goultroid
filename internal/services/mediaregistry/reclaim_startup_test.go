package mediaregistry

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/services/storage"
)

func TestStartupRecoveryReleasesFreshDeletingClaim(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	registry, reclaimer, db := newReclaimerTest(t, store)
	asset := putReclaimerAsset(t, store, registry, "test", LifecycleTransient)
	if err := reclaimer.RequestReclamation(ctx, ReclamationRequest{
		AssetID: asset.ID, Owner: "test", Lifecycle: LifecycleTransient,
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `
		UPDATE media_reclamation_intents
		SET state = ?, claim_token = 'old-process', claimed_at = ?, updated_at = ?
		WHERE asset_id = ?
	`, ReclamationDeleting, now, now, asset.ID); err != nil {
		t.Fatal(err)
	}

	recovered, err := reclaimer.RecoverClaimsAtStartup(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("startup recovered=%d, want 1", recovered)
	}
	intent, err := reclaimer.Intent(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.State != ReclamationPending || intent.Attempts != 1 || intent.ClaimToken != "" || intent.ClaimedAt != nil {
		t.Fatalf("startup recovery left invalid intent: %+v", intent)
	}

	stats, err := reclaimer.Reconcile(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deleted != 1 {
		t.Fatalf("startup-recovered intent stats=%+v, want one deletion", stats)
	}
}
