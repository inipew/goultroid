package settings

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestSettingsRepo(t *testing.T) (*SQLiteRepository, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite in-memory db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	repo := NewSQLiteRepository(db)
	if err := repo.InitSchema(context.Background()); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}
	return repo, db
}

func TestSQLiteRepository_Operations(t *testing.T) {
	repo, _ := setupTestSettingsRepo(t)
	ctx := context.Background()

	// 1. Get non-existent setting
	item, err := repo.GetSetting(ctx, "global", 0, "core", "prefix")
	if err != nil {
		t.Fatalf("expected nil error for missing setting, got: %v", err)
	}
	if item != nil {
		t.Fatalf("expected nil item, got %+v", item)
	}

	// 2. Set new setting
	now := time.Now().UTC()
	toSet := &SettingItem{
		ScopeType: "global",
		ScopeID:   0,
		Namespace: "core",
		Key:       "prefix",
		ValueType: "string",
		Value:     ".",
		UpdatedBy: 12345,
		UpdatedAt: now,
	}
	if err := repo.SetSetting(ctx, toSet); err != nil {
		t.Fatalf("failed to set setting: %v", err)
	}

	// 3. Get setting
	item, err = repo.GetSetting(ctx, "global", 0, "core", "prefix")
	if err != nil || item == nil {
		t.Fatalf("expected to find setting, err: %v, item: %+v", err, item)
	}
	if item.Value != "." || item.ValueType != "string" || item.UpdatedBy != 12345 {
		t.Errorf("unexpected setting content: %+v", item)
	}

	// 4. Update setting (generates second audit log)
	toSet.Value = "!"
	toSet.UpdatedBy = 67890
	if err := repo.SetSetting(ctx, toSet); err != nil {
		t.Fatalf("failed to update setting: %v", err)
	}

	item, err = repo.GetSetting(ctx, "global", 0, "core", "prefix")
	if err != nil || item == nil {
		t.Fatalf("failed to get updated setting: %v", err)
	}
	if item.Value != "!" || item.UpdatedBy != 67890 {
		t.Errorf("expected updated value '!', got %s", item.Value)
	}

	// 5. Add another setting in another namespace and chat scope
	chatItem := &SettingItem{
		ScopeType: "chat",
		ScopeID:   -100123456789,
		Namespace: "antispam",
		Key:       "enabled",
		ValueType: "bool",
		Value:     "true",
		UpdatedBy: 12345,
	}
	if err := repo.SetSetting(ctx, chatItem); err != nil {
		t.Fatalf("failed to set chat setting: %v", err)
	}

	// 6. List settings
	listGlobal, err := repo.ListSettings(ctx, "global", 0, "")
	if err != nil || len(listGlobal) != 1 {
		t.Fatalf("expected 1 global setting, got %d (err=%v)", len(listGlobal), err)
	}
	if listGlobal[0].Key != "prefix" {
		t.Errorf("expected prefix key, got %s", listGlobal[0].Key)
	}

	listChat, err := repo.ListSettings(ctx, "chat", -100123456789, "antispam")
	if err != nil || len(listChat) != 1 {
		t.Fatalf("expected 1 chat setting, got %d (err=%v)", len(listChat), err)
	}
	if listChat[0].Key != "enabled" {
		t.Errorf("expected enabled key, got %s", listChat[0].Key)
	}

	// 7. Check audit history
	history, err := repo.GetSettingHistory(ctx, "core", "prefix", 10)
	if err != nil {
		t.Fatalf("failed to get history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history records, got %d", len(history))
	}
	if history[0].OldVal != "." || history[0].NewVal != "!" || history[0].ChangedBy != 67890 {
		t.Errorf("unexpected latest history entry: %+v", history[0])
	}
	if history[1].OldVal != "" || history[1].NewVal != "." || history[1].ChangedBy != 12345 {
		t.Errorf("unexpected initial history entry: %+v", history[1])
	}

	// 8. Delete setting
	if err := repo.DeleteSetting(ctx, "global", 0, "core", "prefix"); err != nil {
		t.Fatalf("failed to delete setting: %v", err)
	}
	deletedItem, err := repo.GetSetting(ctx, "global", 0, "core", "prefix")
	if err != nil || deletedItem != nil {
		t.Fatalf("expected nil after delete, got item=%+v err=%v", deletedItem, err)
	}

	// History should now have 3 records (including delete)
	historyAfterDel, err := repo.GetSettingHistory(ctx, "core", "prefix", 10)
	if err != nil {
		t.Fatalf("failed to get history after delete: %v", err)
	}
	if len(historyAfterDel) != 3 {
		t.Fatalf("expected 3 history records after delete, got %d", len(historyAfterDel))
	}
	if historyAfterDel[0].OldVal != "!" || historyAfterDel[0].NewVal != "" {
		t.Errorf("unexpected deletion history entry: %+v", historyAfterDel[0])
	}
}

func TestSQLiteRepository_BatchAndOutbox(t *testing.T) {
	repo, _ := setupTestSettingsRepo(t)
	ctx := context.Background()

	batch := []*SettingItem{
		{
			ScopeType: "global",
			ScopeID:   0,
			Namespace: "system",
			Key:       "theme",
			ValueType: "string",
			Value:     "dark",
			UpdatedBy: 111,
		},
		{
			ScopeType: "global",
			ScopeID:   0,
			Namespace: "system",
			Key:       "notifications",
			ValueType: "bool",
			Value:     "true",
			UpdatedBy: 111,
		},
		{
			ScopeType: "chat",
			ScopeID:   -100999888,
			Namespace: "pmpermit",
			Key:       "warns",
			ValueType: "int",
			Value:     "5",
			UpdatedBy: 222,
		},
	}

	if err := repo.SetSettingsBatch(ctx, batch); err != nil {
		t.Fatalf("failed to batch insert settings: %v", err)
	}

	item1, err := repo.GetSetting(ctx, "global", 0, "system", "theme")
	if err != nil || item1 == nil || item1.Value != "dark" {
		t.Errorf("item1 mismatch: %+v, err: %v", item1, err)
	}

	// Verify outbox
	outbox, err := repo.ListPendingOutbox(ctx, 10)
	if err != nil {
		t.Fatalf("failed to list pending outbox: %v", err)
	}
	if len(outbox) != 3 {
		t.Fatalf("expected 3 outbox entries, got %d", len(outbox))
	}

	if err := repo.MarkOutboxProcessed(ctx, outbox[0].ID); err != nil {
		t.Fatalf("failed to mark outbox processed: %v", err)
	}

	outboxAfter, err := repo.ListPendingOutbox(ctx, 10)
	if err != nil {
		t.Fatalf("failed to list pending outbox: %v", err)
	}
	if len(outboxAfter) != 2 {
		t.Fatalf("expected 2 outbox entries remaining, got %d", len(outboxAfter))
	}
}

func TestSQLiteRepository_GetEffectiveSetting(t *testing.T) {
	repo, _ := setupTestSettingsRepo(t)
	ctx := context.Background()

	// Global setting
	_ = repo.SetSetting(ctx, &SettingItem{
		ScopeType: "global", ScopeID: 0,
		Namespace: "test", Key: "foo",
		ValueType: "string", Value: "global_val",
	})

	// User setting
	_ = repo.SetSetting(ctx, &SettingItem{
		ScopeType: "user", ScopeID: 100,
		Namespace: "test", Key: "foo",
		ValueType: "string", Value: "user_val",
	})

	// Chat setting
	_ = repo.SetSetting(ctx, &SettingItem{
		ScopeType: "chat", ScopeID: 500,
		Namespace: "test", Key: "foo",
		ValueType: "string", Value: "chat_val",
	})

	// Effective with chat 500 and user 100 -> Chat takes precedence (priority 1)
	eff, err := repo.GetEffectiveSetting(ctx, "test", "foo", 500, 100)
	if err != nil || eff == nil || eff.Value != "chat_val" {
		t.Fatalf("expected chat_val, got %+v (err=%v)", eff, err)
	}

	// Effective with chat 0 and user 100 -> User takes precedence (priority 2)
	effUser, err := repo.GetEffectiveSetting(ctx, "test", "foo", 0, 100)
	if err != nil || effUser == nil || effUser.Value != "user_val" {
		t.Fatalf("expected user_val, got %+v (err=%v)", effUser, err)
	}

	// Effective with chat 0 and user 0 -> Global takes precedence (priority 3)
	effGlobal, err := repo.GetEffectiveSetting(ctx, "test", "foo", 0, 0)
	if err != nil || effGlobal == nil || effGlobal.Value != "global_val" {
		t.Fatalf("expected global_val, got %+v (err=%v)", effGlobal, err)
	}
}
