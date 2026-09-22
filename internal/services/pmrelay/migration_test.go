package pmrelay

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

type p6hMigrationProvider []database.Migration

func (p p6hMigrationProvider) Migrations() []database.Migration {
	return append([]database.Migration(nil), p...)
}

func TestMigrationCreatesRelaySchemaIdempotently(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for i := 0; i < 2; i++ {
		if err := database.RunFeatureMigrations(ctx, db, MigrationProvider{}); err != nil {
			t.Fatalf("RunFeatureMigrations(%d) error = %v", i, err)
		}
	}
	if err := (migration001{}).VerifySchema(ctx, db); err != nil {
		t.Fatalf("migration001 VerifySchema() error = %v", err)
	}
	if err := (migration002{}).VerifySchema(ctx, db); err != nil {
		t.Fatalf("migration002 VerifySchema() error = %v", err)
	}
	if err := (migration003{}).VerifySchema(ctx, db); err != nil {
		t.Fatalf("migration003 VerifySchema() error = %v", err)
	}
	if err := (migration004{}).VerifySchema(ctx, db); err != nil {
		t.Fatalf("migration004 VerifySchema() error = %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM feature_schema_migrations WHERE id = 'pmrelay.001'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("pmrelay.001 migration records = %d, want 1", count)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM feature_schema_migrations WHERE id = 'pmrelay.002'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("pmrelay.002 migration records = %d, want 1", count)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM feature_schema_migrations WHERE id = 'pmrelay.003'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("pmrelay.003 migration records = %d, want 1", count)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM feature_schema_migrations WHERE id = 'pmrelay.004'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("pmrelay.004 migration records = %d, want 1", count)
	}
}


func TestMigration003BackfillsExistingAudienceMembership(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := (migration001{}).Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, userID := range []int64{300, 100, 200} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO assistant_audience_members (user_id, sources, first_seen_at, last_seen_at)
			VALUES (?, ?, ?, ?)
		`, userID, int64(AudienceSourceStart), now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := (migration003{}).Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := (migration003{}).VerifySchema(ctx, db); err != nil {
		t.Fatal(err)
	}

	repo := NewSQLiteRepository(db)
	snapshot, err := repo.SnapshotAudience(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Total != 3 || snapshot.MaxSequence != 3 {
		t.Fatalf("backfilled snapshot=%+v, want total=3 max_sequence=3", snapshot)
	}
	page, next, err := repo.ListAudienceSnapshot(ctx, snapshot, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if next != 3 || len(page) != 3 ||
		page[0].UserID != 100 || page[1].UserID != 200 || page[2].UserID != 300 {
		t.Fatalf("backfilled page=%+v next=%d", page, next)
	}
}


func TestMigration004SeedsDisabledFailClosedForceSub(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := (migration004{}).Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := (migration004{}).VerifySchema(ctx, db); err != nil {
		t.Fatal(err)
	}

	var (
		enabled      int
		username     string
		joinURL      string
		failureMode  string
		revision     int64
	)
	if err := db.QueryRowContext(ctx, `
		SELECT enabled, channel_username, join_url, failure_mode, revision
		FROM pm_relay_force_sub_config WHERE singleton_id = 1
	`).Scan(&enabled, &username, &joinURL, &failureMode, &revision); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 || username != "" || joinURL != "" ||
		failureMode != string(ForceSubFailClosed) || revision != 1 {
		t.Fatalf("seeded force-sub config enabled=%d username=%q join=%q mode=%q revision=%d",
			enabled, username, joinURL, failureMode, revision)
	}
}


func TestP6HMigrationUpgradeFromP6APreservesDurableRelayState(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(
		ctx,
		db,
		p6hMigrationProvider{migration001{}},
	); err != nil {
		t.Fatalf("apply P6-A migration: %v", err)
	}

	base := time.Date(2026, 9, 22, 7, 0, 0, 0, time.UTC)
	repo := NewSQLiteRepository(db)
	mapping := Mapping{
		OwnerChatID: 7, OwnerMessageID: 500,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: base, ExpiresAt: base.Add(DefaultMappingRetention),
	}
	if _, err := repo.EnsureMapping(ctx, mapping); err != nil {
		t.Fatal(err)
	}
	delivery := DeliveryIntent{
		DeliveryKey: DeliveryKey{
			Direction: DeliveryVisitorToOwner,
			SourceChatID: 42,
			SourceMessageID: 11,
		},
		TargetChatID: 7,
		RandomID: 777,
		CreatedAt: base,
		UpdatedAt: base,
		ExpiresAt: base.Add(DefaultDeliveryRetention),
	}
	if _, err := repo.EnsureDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO assistant_audience_members (
			user_id, sources, first_seen_at, last_seen_at
		) VALUES (?, ?, ?, ?)
	`, int64(42), int64(AudienceSourceRelay), base, base); err != nil {
		t.Fatal(err)
	}

	if err := database.RunFeatureMigrations(ctx, db, MigrationProvider{}); err != nil {
		t.Fatalf("upgrade P6-A -> P6-G schema: %v", err)
	}

	if got, err := repo.GetMapping(ctx, 7, 500); err != nil || got != mapping {
		t.Fatalf("mapping after upgrade=%+v err=%v, want %+v", got, err, mapping)
	}
	gotDelivery, err := repo.GetDelivery(ctx, delivery.DeliveryKey)
	if err != nil {
		t.Fatal(err)
	}
	if gotDelivery.RandomID != 777 || gotDelivery.TargetChatID != 7 {
		t.Fatalf("delivery after upgrade=%+v", gotDelivery)
	}
	member, err := repo.GetAudience(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if member.Sources&AudienceSourceRelay == 0 {
		t.Fatalf("audience after upgrade=%+v", member)
	}

	snapshot, err := repo.SnapshotAudience(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Total != 1 || snapshot.MaxSequence != 1 {
		t.Fatalf("backfilled audience snapshot=%+v, want total=1 max_sequence=1", snapshot)
	}
	forceSub, err := repo.GetForceSubConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if forceSub.Enabled || forceSub.FailureMode != ForceSubFailClosed || forceSub.Revision != 1 {
		t.Fatalf("force-sub default after upgrade=%+v", forceSub)
	}

	var migrationCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM feature_schema_migrations
		WHERE id IN ('pmrelay.001', 'pmrelay.002', 'pmrelay.003', 'pmrelay.004')
	`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 4 {
		t.Fatalf("PM Relay migration records=%d, want 4", migrationCount)
	}
}
