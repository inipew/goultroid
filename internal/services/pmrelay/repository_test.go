package pmrelay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

func newTestRepository(t *testing.T, limits Limits) (*SQLiteRepository, *database.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, MigrationProvider{}); err != nil {
		t.Fatalf("RunFeatureMigrations() error = %v", err)
	}
	return NewSQLiteRepositoryWithLimits(db, limits), db
}

func TestSQLiteMappingIdentityRestartAndCapacity(t *testing.T) {
	ctx := context.Background()
	repo, db := newTestRepository(t, Limits{Mappings: 1, Deliveries: 8, Audience: 8})
	base := time.Date(2026, 9, 22, 4, 0, 0, 0, time.UTC)
	mapping := Mapping{
		OwnerChatID: 7, OwnerMessageID: 101,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: base, ExpiresAt: base.Add(time.Hour),
	}
	created, err := repo.EnsureMapping(ctx, mapping)
	if err != nil {
		t.Fatal(err)
	}
	if !sameMappingIdentity(created, mapping) {
		t.Fatalf("created mapping = %+v", created)
	}

	// Same durable identity is idempotent even if the caller reconstructs a
	// different retention timestamp after restart.
	restarted := NewSQLiteRepositoryWithLimits(db, Limits{Mappings: 1, Deliveries: 8, Audience: 8})
	duplicate := mapping
	duplicate.ExpiresAt = base.Add(2 * time.Hour)
	got, err := restarted.EnsureMapping(ctx, duplicate)
	if err != nil {
		t.Fatalf("EnsureMapping(duplicate) error = %v", err)
	}
	if !got.ExpiresAt.Equal(mapping.ExpiresAt) {
		t.Fatalf("duplicate changed authoritative expiry: got %v want %v", got.ExpiresAt, mapping.ExpiresAt)
	}

	conflict := mapping
	conflict.OwnerMessageID++
	if _, err := restarted.EnsureMapping(ctx, conflict); !errors.Is(err, ErrMappingConflict) {
		t.Fatalf("EnsureMapping(conflict) error = %v, want %v", err, ErrMappingConflict)
	}

	other := Mapping{
		OwnerChatID: 7, OwnerMessageID: 102,
		VisitorUserID: 43, VisitorMessageID: 12,
		CreatedAt: base, ExpiresAt: base.Add(time.Hour),
	}
	if _, err := restarted.EnsureMapping(ctx, other); !errors.Is(err, ErrMappingCapacity) {
		t.Fatalf("EnsureMapping(at capacity) error = %v, want %v", err, ErrMappingCapacity)
	}
	if _, err := restarted.GetMapping(ctx, mapping.OwnerChatID, mapping.OwnerMessageID); err != nil {
		t.Fatalf("live mapping was evicted at capacity: %v", err)
	}

	pruned, err := restarted.PruneExpiredMappings(ctx, base.Add(2*time.Hour), 64)
	if err != nil || pruned != 1 {
		t.Fatalf("PruneExpiredMappings() = %d, %v", pruned, err)
	}
	if _, err := restarted.EnsureMapping(ctx, other); err != nil {
		t.Fatalf("EnsureMapping(after prune) error = %v", err)
	}
}

func TestSQLiteDeliveryIntentReusesRandomIDAndFencesClaims(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 1, Audience: 8})
	base := time.Date(2026, 9, 22, 5, 0, 0, 0, time.UTC)
	intent := DeliveryIntent{
		DeliveryKey: DeliveryKey{
			Direction: DeliveryVisitorToOwner, SourceChatID: 42, SourceMessageID: 11,
		},
		TargetChatID: 7,
		RandomID:     111,
		CreatedAt:    base,
		UpdatedAt:    base,
		ExpiresAt:    base.Add(time.Hour),
	}
	created, err := repo.EnsureDelivery(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if created.RandomID != 111 {
		t.Fatalf("created random id = %d", created.RandomID)
	}

	duplicate := intent
	duplicate.RandomID = 999
	stable, err := repo.EnsureDelivery(ctx, duplicate)
	if err != nil {
		t.Fatalf("EnsureDelivery(duplicate) error = %v", err)
	}
	if stable.RandomID != 111 {
		t.Fatalf("duplicate replaced durable random id: got %d want 111", stable.RandomID)
	}

	claimed, err := repo.ClaimDelivery(ctx, intent.DeliveryKey, base, "claim-a", base.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Attempts != 1 || claimed.ClaimID != "claim-a" {
		t.Fatalf("claimed delivery = %+v", claimed)
	}
	if _, err := repo.ClaimDelivery(ctx, intent.DeliveryKey, base.Add(time.Minute), "claim-b", base.Add(3*time.Minute)); !errors.Is(err, ErrDeliveryClaimed) {
		t.Fatalf("concurrent ClaimDelivery() error = %v, want %v", err, ErrDeliveryClaimed)
	}

	claimed, err = repo.ClaimDelivery(ctx, intent.DeliveryKey, base.Add(3*time.Minute), "claim-b", base.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("ClaimDelivery(after lease expiry) error = %v", err)
	}
	if claimed.Attempts != 2 || claimed.ClaimID != "claim-b" {
		t.Fatalf("reclaimed delivery = %+v", claimed)
	}
	if err := repo.ReleaseDelivery(ctx, intent.DeliveryKey, "claim-b", base.Add(4*time.Minute), "temporary failure"); err != nil {
		t.Fatalf("ReleaseDelivery() error = %v", err)
	}

	if _, err := repo.ClaimDelivery(ctx, intent.DeliveryKey, base.Add(5*time.Minute), "claim-c", base.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	committed, err := repo.CommitDelivery(ctx, intent.DeliveryKey, "claim-c", 501, base.Add(5*time.Minute+30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !committed.Completed() || committed.TargetMessageID != 501 || committed.ClaimID != "" {
		t.Fatalf("committed delivery = %+v", committed)
	}
	// Commit is idempotent for an already persisted successful send.
	again, err := repo.CommitDelivery(ctx, intent.DeliveryKey, "claim-c", 501, base.Add(6*time.Minute))
	if err != nil {
		t.Fatalf("CommitDelivery(replay) error = %v", err)
	}
	if again.TargetMessageID != 501 {
		t.Fatalf("replayed commit = %+v", again)
	}

	other := intent
	other.SourceMessageID = 12
	other.RandomID = 222
	if _, err := repo.EnsureDelivery(ctx, other); !errors.Is(err, ErrDeliveryCapacity) {
		t.Fatalf("EnsureDelivery(at capacity) error = %v, want %v", err, ErrDeliveryCapacity)
	}
	if _, err := repo.GetDelivery(ctx, intent.DeliveryKey); err != nil {
		t.Fatalf("live delivery was evicted at capacity: %v", err)
	}

	pruned, err := repo.PruneExpiredDeliveries(ctx, base.Add(2*time.Hour), 64)
	if err != nil || pruned != 1 {
		t.Fatalf("PruneExpiredDeliveries() = %d, %v", pruned, err)
	}
	if _, err := repo.EnsureDelivery(ctx, other); err != nil {
		t.Fatalf("EnsureDelivery(after prune) error = %v", err)
	}
}

func TestSQLiteDeliverySourceCannotRetargetOnRetry(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	base := time.Date(2026, 9, 22, 6, 0, 0, 0, time.UTC)
	intent := DeliveryIntent{
		DeliveryKey:  DeliveryKey{Direction: DeliveryOwnerToVisitor, SourceChatID: 7, SourceMessageID: 91},
		TargetChatID: 42,
		RandomID:     123,
		CreatedAt:    base,
		UpdatedAt:    base,
		ExpiresAt:    base.Add(time.Hour),
	}
	if _, err := repo.EnsureDelivery(ctx, intent); err != nil {
		t.Fatal(err)
	}
	intent.TargetChatID = 43
	intent.RandomID = 456
	if _, err := repo.EnsureDelivery(ctx, intent); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatalf("retargeted EnsureDelivery() error = %v, want %v", err, ErrDeliveryConflict)
	}
}

func TestSQLiteDeliveryPruneDoesNotDeleteActiveLease(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	base := time.Date(2026, 9, 22, 7, 0, 0, 0, time.UTC)
	intent := DeliveryIntent{
		DeliveryKey:  DeliveryKey{Direction: DeliveryVisitorToOwner, SourceChatID: 42, SourceMessageID: 77},
		TargetChatID: 7,
		RandomID:     987,
		CreatedAt:    base,
		UpdatedAt:    base,
		ExpiresAt:    base.Add(time.Minute),
	}
	if _, err := repo.EnsureDelivery(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimDelivery(ctx, intent.DeliveryKey, base, "active", base.Add(DeliveryClaimTTL)); err != nil {
		t.Fatal(err)
	}
	pruned, err := repo.PruneExpiredDeliveries(ctx, base.Add(2*time.Minute), 64)
	if err != nil || pruned != 0 {
		t.Fatalf("active lease pruned = %d, %v", pruned, err)
	}
	pruned, err = repo.PruneExpiredDeliveries(ctx, base.Add(DeliveryClaimTTL+time.Minute), 64)
	if err != nil || pruned != 1 {
		t.Fatalf("expired lease prune = %d, %v", pruned, err)
	}
}

func TestSQLiteAudienceMergesSourcesAndFailsClosedAtCapacity(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 1})
	base := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	member, err := repo.TouchAudience(ctx, AudienceTouch{UserID: 42, Source: AudienceSourceRelay, SeenAt: base})
	if err != nil {
		t.Fatal(err)
	}
	if member.Sources != AudienceSourceRelay {
		t.Fatalf("initial sources = %d", member.Sources)
	}
	member, err = repo.TouchAudience(ctx, AudienceTouch{UserID: 42, Source: AudienceSourceStart, SeenAt: base.Add(time.Hour)})
	if err != nil {
		t.Fatalf("TouchAudience(existing at capacity) error = %v", err)
	}
	wantSources := AudienceSourceRelay | AudienceSourceStart
	if member.Sources != wantSources || !member.FirstSeenAt.Equal(base) || !member.LastSeenAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("merged member = %+v", member)
	}

	if _, err := repo.TouchAudience(ctx, AudienceTouch{UserID: 43, Source: AudienceSourceRelay, SeenAt: base}); !errors.Is(err, ErrAudienceCapacity) {
		t.Fatalf("TouchAudience(at capacity) error = %v, want %v", err, ErrAudienceCapacity)
	}
	if _, err := repo.GetAudience(ctx, 42); err != nil {
		t.Fatalf("live audience member was evicted at capacity: %v", err)
	}

	listed, err := repo.ListAudience(ctx, 0, 10)
	if err != nil || len(listed) != 1 || listed[0].UserID != 42 {
		t.Fatalf("ListAudience() = %+v, %v", listed, err)
	}
	pruned, err := repo.PruneAudienceBefore(ctx, base.Add(2*time.Hour), 64)
	if err != nil || pruned != 1 {
		t.Fatalf("PruneAudienceBefore() = %d, %v", pruned, err)
	}
	if _, err := repo.TouchAudience(ctx, AudienceTouch{UserID: 43, Source: AudienceSourceRelay, SeenAt: base.Add(2 * time.Hour)}); err != nil {
		t.Fatalf("TouchAudience(after prune) error = %v", err)
	}
}

func TestDomainRetentionBoundsRejectUnboundedRows(t *testing.T) {
	base := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	if _, err := (Mapping{
		OwnerChatID: 7, OwnerMessageID: 1, VisitorUserID: 42, VisitorMessageID: 2,
		CreatedAt: base, ExpiresAt: base.Add(MaxMappingRetention + time.Second),
	}).Normalize(); !errors.Is(err, ErrInvalidMapping) {
		t.Fatalf("Mapping.Normalize() error = %v, want %v", err, ErrInvalidMapping)
	}
	if _, err := (DeliveryIntent{
		DeliveryKey:  DeliveryKey{Direction: DeliveryVisitorToOwner, SourceChatID: 42, SourceMessageID: 2},
		TargetChatID: 7, RandomID: 5, CreatedAt: base, UpdatedAt: base,
		ExpiresAt: base.Add(MaxDeliveryRetention + time.Second),
	}).Normalize(); !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("DeliveryIntent.Normalize() error = %v, want %v", err, ErrInvalidDelivery)
	}
}


func TestSQLiteVisitorBlocksAreDurableIdempotentAndBounded(t *testing.T) {
	ctx := context.Background()
	repo, db := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 1})
	base := time.Date(2026, 9, 22, 19, 0, 0, 0, time.UTC)

	block, err := repo.SetVisitorBlock(ctx, VisitorBlock{
		VisitorUserID: 42,
		BlockedAt:     base,
		Reason:        "spam",
	})
	if err != nil {
		t.Fatal(err)
	}
	if block.VisitorUserID != 42 || block.Reason != "spam" || !block.BlockedAt.Equal(base) {
		t.Fatalf("block=%+v", block)
	}

	restarted := NewSQLiteRepositoryWithLimits(db, Limits{Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 1})
	got, err := restarted.GetVisitorBlock(ctx, 42)
	if err != nil {
		t.Fatalf("GetVisitorBlock(restart) error=%v", err)
	}
	if got.Reason != "spam" {
		t.Fatalf("restart block=%+v", got)
	}

	updatedAt := base.Add(time.Minute)
	got, err = restarted.SetVisitorBlock(ctx, VisitorBlock{
		VisitorUserID: 42,
		BlockedAt:     updatedAt,
		Reason:        "repeat abuse",
	})
	if err != nil {
		t.Fatalf("SetVisitorBlock(update) error=%v", err)
	}
	if got.Reason != "repeat abuse" || !got.BlockedAt.Equal(updatedAt) {
		t.Fatalf("updated block=%+v", got)
	}

	if _, err := restarted.SetVisitorBlock(ctx, VisitorBlock{
		VisitorUserID: 43,
		BlockedAt:     base,
	}); !errors.Is(err, ErrBlockCapacity) {
		t.Fatalf("SetVisitorBlock(at capacity) error=%v, want %v", err, ErrBlockCapacity)
	}
	if _, err := restarted.GetVisitorBlock(ctx, 42); err != nil {
		t.Fatalf("existing block was evicted: %v", err)
	}

	list, err := restarted.ListVisitorBlocks(ctx, 0, 10)
	if err != nil || len(list) != 1 || list[0].VisitorUserID != 42 {
		t.Fatalf("ListVisitorBlocks()=%+v err=%v", list, err)
	}
	if count, err := restarted.CountVisitorBlocks(ctx); err != nil || count != 1 {
		t.Fatalf("CountVisitorBlocks()=%d err=%v", count, err)
	}

	deleted, err := restarted.DeleteVisitorBlock(ctx, 42)
	if err != nil || !deleted {
		t.Fatalf("DeleteVisitorBlock()=%v err=%v", deleted, err)
	}
	deleted, err = restarted.DeleteVisitorBlock(ctx, 42)
	if err != nil || deleted {
		t.Fatalf("DeleteVisitorBlock(idempotent)=%v err=%v", deleted, err)
	}
	if _, err := restarted.GetVisitorBlock(ctx, 42); !errors.Is(err, ErrBlockNotFound) {
		t.Fatalf("GetVisitorBlock(after delete) error=%v, want %v", err, ErrBlockNotFound)
	}
	if _, err := restarted.SetVisitorBlock(ctx, VisitorBlock{VisitorUserID: 43, BlockedAt: base}); err != nil {
		t.Fatalf("SetVisitorBlock(after capacity release) error=%v", err)
	}
}

func TestVisitorBlockRejectsInvalidRows(t *testing.T) {
	base := time.Date(2026, 9, 22, 20, 0, 0, 0, time.UTC)
	if _, err := (VisitorBlock{VisitorUserID: 0, BlockedAt: base}).Normalize(); !errors.Is(err, ErrInvalidBlock) {
		t.Fatalf("VisitorBlock.Normalize(zero user) error=%v, want %v", err, ErrInvalidBlock)
	}
	if _, err := (VisitorBlock{
		VisitorUserID: 42,
		BlockedAt:     base,
		Reason:        string(make([]byte, MaxBlockReasonBytes+1)),
	}).Normalize(); !errors.Is(err, ErrInvalidBlock) {
		t.Fatalf("VisitorBlock.Normalize(long reason) error=%v, want %v", err, ErrInvalidBlock)
	}
}


func TestSQLiteAudienceSnapshotUsesStableMembershipKeyset(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 8})
	base := time.Now().UTC().Add(-time.Minute)

	for _, userID := range []int64{100, 300} {
		if _, err := repo.TouchAudience(ctx, AudienceTouch{
			UserID: userID, Source: AudienceSourceStart, SeenAt: base,
		}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := repo.SnapshotAudience(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Total != 2 || snapshot.MaxSequence <= 0 {
		t.Fatalf("SnapshotAudience()=%+v", snapshot)
	}

	// A member first seen after snapshot creation must never leak into the
	// already-captured broadcast membership, regardless of Telegram user ID.
	if _, err := repo.TouchAudience(ctx, AudienceTouch{
		UserID: 200, Source: AudienceSourceInline, SeenAt: base.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	// Updating an existing member preserves its membership sequence.
	if _, err := repo.TouchAudience(ctx, AudienceTouch{
		UserID: 100, Source: AudienceSourceDeepLink, SeenAt: base.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	var (
		cursor int64
		got    []int64
	)
	for {
		page, next, err := repo.ListAudienceSnapshot(ctx, snapshot, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, member := range page {
			got = append(got, member.UserID)
		}
		cursor = next
		if len(page) == 0 || cursor >= snapshot.MaxSequence {
			break
		}
	}
	if len(got) != 2 || got[0] != 100 || got[1] != 300 {
		t.Fatalf("snapshot members=%v, want [100 300]", got)
	}

	current, err := repo.SnapshotAudience(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Total != 3 || current.MaxSequence <= snapshot.MaxSequence {
		t.Fatalf("current snapshot=%+v, previous=%+v", current, snapshot)
	}
	member, err := repo.GetAudience(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	wantSources := AudienceSourceStart | AudienceSourceDeepLink
	if member.Sources != wantSources {
		t.Fatalf("existing member sources=%d, want %d", member.Sources, wantSources)
	}
}
