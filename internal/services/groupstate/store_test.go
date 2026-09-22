package groupstate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

type storeRoleResolver struct{}

func (*storeRoleResolver) ResolveGroupRole(_ context.Context, req core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	return core.GroupRoleSnapshot{
		Principal: core.GroupActorPrincipal{
			UserID:   req.UserID,
			Role:     core.GroupActorRoleAdministrator,
			Verified: true,
		},
	}, nil
}

func (*storeRoleResolver) ResolveGroupRoleFresh(_ context.Context, req core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	return core.GroupRoleSnapshot{
		Principal: core.GroupActorPrincipal{
			UserID:   req.UserID,
			Role:     core.GroupActorRoleAdministrator,
			Verified: true,
		},
	}, nil
}

var storeAdminRequirement = core.GroupAuthorizationRequirement{
	Level: core.GroupAuthorizationAdministrator,
}

func newTestStore(t *testing.T, limits Limits) (*SQLiteStore, *database.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, MigrationProvider{}); err != nil {
		t.Fatalf("RunFeatureMigrations() error=%v", err)
	}
	store, err := NewSQLiteStoreWithLimits(db, limits)
	if err != nil {
		t.Fatal(err)
	}
	return store, db
}

func stateContext(store core.GroupStateStore, chatID int64, userID int64) *core.Context {
	ctx := &core.Context{
		Ctx:        context.Background(),
		Source:     core.ExecutionAssistant,
		Chat:       &core.Chat{ID: chatID, Type: "supergroup"},
		PeerID:     &tg.InputPeerChannel{ChannelID: chatID, AccessHash: chatID + 100},
		Sender:     &core.User{ID: userID},
		GroupRoles: &storeRoleResolver{},
	}
	core.AttachGroupStateStore(ctx, store)
	return ctx
}

func TestSQLiteStoreCASRestartAndChatIsolation(t *testing.T) {
	store, db := newTestStore(t, Limits{MaxEntries: 8, CleanupBatch: 2})
	ctx100 := stateContext(store, 100, 42)

	created, err := ctx100.CompareAndSwapGroupState(
		storeAdminRequirement,
		" Moderation ",
		" Mode ",
		0,
		[]byte("strict"),
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.Namespace != "moderation" || created.Key != "mode" || string(created.Value) != "strict" {
		t.Fatalf("created=%+v", created)
	}

	if _, err := ctx100.CompareAndSwapGroupState(
		storeAdminRequirement,
		"moderation",
		"mode",
		0,
		[]byte("duplicate-create"),
		0,
	); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("duplicate create error=%v, want ErrStateConflict", err)
	}

	updated, err := ctx100.CompareAndSwapGroupState(
		storeAdminRequirement,
		"moderation",
		"mode",
		created.Revision,
		[]byte("relaxed"),
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.UpdatedBy != 42 || string(updated.Value) != "relaxed" {
		t.Fatalf("updated=%+v", updated)
	}

	if _, err := ctx100.CompareAndSwapGroupState(
		storeAdminRequirement,
		"moderation",
		"mode",
		1,
		[]byte("stale"),
		0,
	); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale CAS error=%v, want ErrStateConflict", err)
	}

	ctx200 := stateContext(store, 200, 42)
	otherChat, err := ctx200.CompareAndSwapGroupState(
		storeAdminRequirement,
		"moderation",
		"mode",
		0,
		[]byte("other"),
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if otherChat.Revision != 1 {
		t.Fatalf("other chat revision=%d, want 1", otherChat.Revision)
	}

	restarted, err := NewSQLiteStoreWithLimits(db, Limits{MaxEntries: 8, CleanupBatch: 2})
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Get(context.Background(), core.GroupStateKey{ChatID: 100, Namespace: "MODERATION", Key: "MODE"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || string(got.Value) != "relaxed" {
		t.Fatalf("restart state=%+v", got)
	}
	other, err := restarted.Get(context.Background(), core.GroupStateKey{ChatID: 200, Namespace: "moderation", Key: "mode"})
	if err != nil {
		t.Fatal(err)
	}
	if string(other.Value) != "other" {
		t.Fatalf("chat isolation state=%+v", other)
	}
}

func TestSQLiteStoreDeleteUsesExactRevision(t *testing.T) {
	store, _ := newTestStore(t, Limits{MaxEntries: 8, CleanupBatch: 2})
	ctx := stateContext(store, 1, 7)
	created, err := ctx.CompareAndSwapGroupState(
		storeAdminRequirement,
		"filters",
		"enabled",
		0,
		[]byte("true"),
		0,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := ctx.DeleteGroupState(
		storeAdminRequirement,
		"filters",
		"enabled",
		created.Revision+1,
	); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale delete error=%v, want ErrStateConflict", err)
	}
	if _, err := store.Get(context.Background(), created.GroupStateKey); err != nil {
		t.Fatalf("stale delete removed state: %v", err)
	}

	if err := ctx.DeleteGroupState(
		storeAdminRequirement,
		"filters",
		"enabled",
		created.Revision,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), created.GroupStateKey); !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("get after delete error=%v, want ErrStateNotFound", err)
	}
}

func TestSQLiteStoreCapacityLazilyReclaimsOnlyExpiredState(t *testing.T) {
	store, _ := newTestStore(t, Limits{MaxEntries: 2, CleanupBatch: 1})
	ctx1 := stateContext(store, 1, 7)
	ctx2 := stateContext(store, 2, 7)

	expiring, err := ctx1.CompareAndSwapGroupState(
		storeAdminRequirement,
		"manager",
		"temporary",
		0,
		[]byte("old"),
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	live, err := ctx2.CompareAndSwapGroupState(
		storeAdminRequirement,
		"manager",
		"durable",
		0,
		[]byte("keep"),
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := store.Count(context.Background()); err != nil || count != 2 {
		t.Fatalf("initial count=%d err=%v", count, err)
	}

	if expiring.ExpiresAt == nil {
		t.Fatal("expiring state has no expiry")
	}
	store.now = func() time.Time { return expiring.ExpiresAt.Add(time.Second) }
	if _, err := store.Get(context.Background(), expiring.GroupStateKey); !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("expired get error=%v, want ErrStateNotFound", err)
	}

	ctx3 := stateContext(store, 3, 8)
	if _, err := ctx3.CompareAndSwapGroupState(
		storeAdminRequirement,
		"manager",
		"new",
		0,
		[]byte("new"),
		0,
	); err != nil {
		t.Fatalf("create after lazy expiry reclamation: %v", err)
	}
	if count, err := store.Count(context.Background()); err != nil || count != 2 {
		t.Fatalf("count after reclamation=%d err=%v", count, err)
	}
	if got, err := store.Get(context.Background(), live.GroupStateKey); err != nil || string(got.Value) != "keep" {
		t.Fatalf("live state was evicted: %+v err=%v", got, err)
	}

	ctx4 := stateContext(store, 4, 8)
	if _, err := ctx4.CompareAndSwapGroupState(
		storeAdminRequirement,
		"manager",
		"overflow",
		0,
		[]byte("no"),
		0,
	); !errors.Is(err, ErrStateCapacity) {
		t.Fatalf("capacity error=%v, want ErrStateCapacity", err)
	}
}

func TestSQLiteStorePruneExpiredIsBounded(t *testing.T) {
	store, _ := newTestStore(t, Limits{MaxEntries: 4, CleanupBatch: 1})
	var latestExpiry time.Time
	for chatID := int64(1); chatID <= 2; chatID++ {
		ctx := stateContext(store, chatID, 7)
		record, err := ctx.CompareAndSwapGroupState(
			storeAdminRequirement,
			"temp",
			"x",
			0,
			[]byte("x"),
			time.Minute,
		)
		if err != nil {
			t.Fatal(err)
		}
		if record.ExpiresAt != nil && record.ExpiresAt.After(latestExpiry) {
			latestExpiry = *record.ExpiresAt
		}
	}
	store.now = func() time.Time { return latestExpiry.Add(time.Second) }

	pruned, err := store.PruneExpired(context.Background(), store.now(), 1)
	if err != nil || pruned != 1 {
		t.Fatalf("PruneExpired(limit=1)=%d err=%v", pruned, err)
	}
	if count, err := store.Count(context.Background()); err != nil || count != 1 {
		t.Fatalf("count after bounded prune=%d err=%v", count, err)
	}
	pruned, err = store.PruneExpired(context.Background(), store.now(), 1)
	if err != nil || pruned != 1 {
		t.Fatalf("second PruneExpired=%d err=%v", pruned, err)
	}
}

func TestSQLiteStoreRejectsDirectWriteWithoutOpaqueGrant(t *testing.T) {
	store, _ := newTestStore(t, Limits{MaxEntries: 8, CleanupBatch: 2})
	_, err := store.CompareAndSwap(context.Background(), core.GroupStateWriteGrant{}, core.GroupStateCAS{
		GroupStateKey: core.GroupStateKey{ChatID: 1, Namespace: "manager", Key: "direct"},
		Value:         []byte("no"),
		UpdatedBy:     7,
		UpdatedAt:     time.Now().UTC(),
	})
	if !errors.Is(err, core.ErrGroupAuthorizationDenied) {
		t.Fatalf("direct write error=%v, want ErrGroupAuthorizationDenied", err)
	}
}

func TestSQLiteStoreRejectsOversizedValueAndInvalidLimits(t *testing.T) {
	store, _ := newTestStore(t, Limits{MaxEntries: 8, CleanupBatch: 2})
	_, err := store.CompareAndSwap(context.Background(), core.GroupStateWriteGrant{}, core.GroupStateCAS{
		GroupStateKey: core.GroupStateKey{ChatID: 1, Namespace: "manager", Key: "large"},
		Value:         make([]byte, core.MaxGroupStateValueBytes+1),
		UpdatedBy:     7,
	})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("oversized value error=%v, want ErrInvalidState", err)
	}
	if _, err := NewSQLiteStoreWithLimits(store.db, Limits{MaxEntries: HardMaxEntries + 1}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("invalid max entries error=%v, want ErrInvalidState", err)
	}
}

func TestSQLiteSchemaRejectsOversizedValueBypass(t *testing.T) {
	store, db := newTestStore(t, Limits{MaxEntries: 8, CleanupBatch: 2})
	if store == nil {
		t.Fatal("store is nil")
	}

	_, err := db.ExecContext(context.Background(), `
		INSERT INTO assistant_group_state (
			chat_id, namespace, key, value, revision, updated_by, updated_at
		) VALUES (?, ?, ?, ?, 1, ?, ?)
	`, 1, "manager", "raw", make([]byte, core.MaxGroupStateValueBytes+1), 7, time.Now().UTC())
	if err == nil {
		t.Fatal("raw SQL oversized group state bypassed database value bound")
	}

	var count int
	if err := db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM assistant_group_state
		WHERE chat_id = 1 AND namespace = 'manager' AND key = 'raw'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("oversized raw row persisted count=%d", count)
	}
}

type migration001OnlyProvider struct{}

func (migration001OnlyProvider) Migrations() []database.Migration {
	return []database.Migration{migration001{}}
}

func TestP7FMigration001UpgradeToValueBound(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if got := (migration001{}).Checksum(); got != "4963bf013a3370d9ba521c005c408dfbbe51064aeb6d976d68f2dd19cc2d4f5e" {
		t.Fatalf("assistant_group_state.001 checksum changed: %s", got)
	}
	if err := database.RunFeatureMigrations(ctx, db, migration001OnlyProvider{}); err != nil {
		t.Fatalf("apply migration001 only: %v", err)
	}

	var applied int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM feature_schema_migrations
		WHERE id = 'assistant_group_state.001'
	`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("migration001 applied count=%d, want 1", applied)
	}

	if err := database.RunFeatureMigrations(ctx, db, MigrationProvider{}); err != nil {
		t.Fatalf("upgrade migration001 -> current provider: %v", err)
	}

	for _, trigger := range []string{
		"trg_assistant_group_state_value_insert",
		"trg_assistant_group_state_value_update",
		"trg_assistant_group_state_coordinate_insert",
		"trg_assistant_group_state_coordinate_update",
	} {
		var count int
		if err := db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM sqlite_master
			WHERE type = 'trigger' AND name = ?
		`, trigger).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("upgrade missing trigger %s", trigger)
		}
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO assistant_group_state (
			chat_id, namespace, key, value, revision, updated_by, updated_at
		) VALUES (?, ?, ?, ?, 1, ?, ?)
	`, 99, "manager", "upgrade", make([]byte, core.MaxGroupStateValueBytes+1), 7, time.Now().UTC())
	if err == nil {
		t.Fatal("post-upgrade database accepted oversized value")
	}
}

func TestSQLiteSchemaRejectsNonCanonicalCoordinateBypass(t *testing.T) {
	_, db := newTestStore(t, Limits{MaxEntries: 8, CleanupBatch: 2})
	now := time.Now().UTC()

	for _, coordinate := range []struct {
		namespace string
		key       string
	}{
		{namespace: "Manager", key: "mode"},
		{namespace: "manager ", key: "mode"},
		{namespace: "manager", key: "bad key"},
		{namespace: "månager", key: "mode"},
	} {
		_, err := db.ExecContext(context.Background(), `
			INSERT INTO assistant_group_state (
				chat_id, namespace, key, value, revision, updated_by, updated_at
			) VALUES (?, ?, ?, ?, 1, ?, ?)
		`, 1, coordinate.namespace, coordinate.key, []byte("x"), 7, now)
		if err == nil {
			t.Fatalf("raw SQL accepted non-canonical coordinate namespace=%q key=%q",
				coordinate.namespace, coordinate.key)
		}
	}

	var count int
	if err := db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM assistant_group_state
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("non-canonical raw rows persisted count=%d", count)
	}
}

func TestNormalizeGroupStateKeyRejectsDisplayStrings(t *testing.T) {
	for _, key := range []core.GroupStateKey{
		{ChatID: 1, Namespace: "manager state", Key: "mode"},
		{ChatID: 1, Namespace: "manager", Key: "møde"},
		{ChatID: 0, Namespace: "manager", Key: "mode"},
	} {
		if _, err := core.NormalizeGroupStateKey(key); err == nil {
			t.Fatalf("NormalizeGroupStateKey(%+v) unexpectedly succeeded", key)
		}
	}

	got, err := core.NormalizeGroupStateKey(core.GroupStateKey{
		ChatID: 1, Namespace: " Manager ", Key: "MODE/V1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != "manager" || got.Key != "mode/v1" {
		t.Fatalf("normalized key=%+v", got)
	}
}
