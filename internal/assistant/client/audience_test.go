package client

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"go.uber.org/zap"
)

func newAssistantAudienceRegistry(t *testing.T) (*pmrelay.Service, *pmrelay.SQLiteRepository) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, pmrelay.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	repo := pmrelay.NewSQLiteRepositoryWithLimits(db, pmrelay.Limits{
		Mappings: 8, Deliveries: 8, Audience: 32, Blocked: 8,
	})
	return pmrelay.NewService(repo, 7), repo
}

func TestAssistantAudienceTargetSourceFreezesMembership(t *testing.T) {
	ctx := context.Background()
	registry, _ := newAssistantAudienceRegistry(t)
	base := time.Now().UTC().Add(-time.Minute)
	for _, userID := range []int64{11, 22} {
		if _, err := registry.TouchAudience(ctx, pmrelay.AudienceTouch{
			UserID: userID, Source: pmrelay.AudienceSourceStart, SeenAt: base,
		}); err != nil {
			t.Fatal(err)
		}
	}

	source, err := newAssistantAudienceTargetSource(ctx, registry)
	if err != nil {
		t.Fatal(err)
	}
	if source.Total() != 2 {
		t.Fatalf("source total=%d, want 2", source.Total())
	}

	// This member belongs only to a later snapshot.
	if _, err := registry.TouchAudience(ctx, pmrelay.AudienceTouch{
		UserID: 15, Source: pmrelay.AudienceSourceInline, SeenAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	var got []int64
	for {
		page, done, err := source.Next(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range page {
			user, ok := target.(*tg.InputPeerUser)
			if !ok {
				t.Fatalf("target type=%T", target)
			}
			got = append(got, user.UserID)
		}
		if done {
			break
		}
	}
	if len(got) != 2 || got[0] != 11 || got[1] != 22 {
		t.Fatalf("snapshot targets=%v, want [11 22]", got)
	}
}

func TestAssistantAudienceTouchMergesEntryPointSources(t *testing.T) {
	ctx := context.Background()
	registry, repo := newAssistantAudienceRegistry(t)
	logger := zap.NewNop()
	for _, source := range []pmrelay.AudienceSource{
		pmrelay.AudienceSourceStart,
		pmrelay.AudienceSourceInline,
		pmrelay.AudienceSourceDeepLink,
		pmrelay.AudienceSourceRelay,
	} {
		touchAssistantAudience(ctx, registry, 42, source, logger)
	}

	member, err := repo.GetAudience(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	want := pmrelay.AudienceSourceStart |
		pmrelay.AudienceSourceInline |
		pmrelay.AudienceSourceDeepLink |
		pmrelay.AudienceSourceRelay
	if member.Sources != want {
		t.Fatalf("audience sources=%d, want %d", member.Sources, want)
	}
	snapshot, err := registry.SnapshotAudience(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Total != 1 {
		t.Fatalf("snapshot total=%d, want 1", snapshot.Total)
	}
}
