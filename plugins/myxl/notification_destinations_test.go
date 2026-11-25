package myxl

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

type m6OnlyMigrationProvider struct{}

func (m6OnlyMigrationProvider) Migrations() []database.Migration {
	return []database.Migration{
		migration001{}, migration002{}, migration003{}, migration004{},
		migration005{}, migration006{}, migration007{}, migration008{},
	}
}

func TestN3_Migration009_FreshAndUpgradeFromM6(t *testing.T) {
	ctx := context.Background()

	// 1. Fresh DB runs through myxl.010
	freshDB, freshRepo := openMyXLTestDB(t)
	var count int
	if err := freshDB.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id = 'myxl.010'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected myxl.010 recorded on fresh db, count=%d err=%v", count, err)
	}
	defCfg, err := freshRepo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		t.Fatalf("get default config on fresh db: %v", err)
	}
	if !defCfg.NotifyOnPartialFailure || defCfg.NotifyOnCancelled || len(defCfg.Destinations) != 0 {
		t.Fatalf("unexpected fresh defaults: %+v", defCfg)
	}

	// 2. Upgrade existing M6 (myxl.008) database with persisted config, run history, and account
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := database.RunFeatureMigrations(ctx, db, m6OnlyMigrationProvider{}); err != nil {
		t.Fatalf("run M6 migrations: %v", err)
	}

	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO myxl_token_maintenance_config (
			id, enabled, interval_seconds, anchor_time, timezone, refresh_window_seconds,
			account_scope, selected_msisdns_json, notify_on_completion, notify_on_failure, updated_at
		) VALUES (1, 1, 43200, '04:00', 'Asia/Jakarta', 2700, 'active', '[]', 1, 0, ?)
	`, now); err != nil {
		t.Fatalf("seed M6 config: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO myxl_token_maintenance_runs (
			trigger_source, status, started_at, completed_at, duration_ms,
			total_accounts, refreshed_count, already_fresh_count, skipped_count,
			failed_count, cancelled_count, error_summary
		) VALUES ('scheduled', 'completed', ?, ?, 150, 2, 1, 1, 0, 0, 0, '')
	`, now, now); err != nil {
		t.Fatalf("seed M6 run: %v", err)
	}
	repo := NewSQLiteRepository(db)
	if err := repo.Save(ctx, &Account{MSISDN: "6281900000001", IsActive: true, RefreshToken: "rt"}); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	// Upgrade to myxl.009
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("upgrade to myxl.009: %v", err)
	}

	upgradedCfg, err := repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		t.Fatalf("get upgraded config: %v", err)
	}
	if !upgradedCfg.Enabled || upgradedCfg.Interval != 12*time.Hour || upgradedCfg.AnchorTime != "04:00" ||
		!upgradedCfg.NotifyOnCompletion || upgradedCfg.NotifyOnFailure ||
		!upgradedCfg.NotifyOnPartialFailure || upgradedCfg.NotifyOnCancelled || len(upgradedCfg.Destinations) != 0 {
		t.Fatalf("upgraded M6 config mismatch: %+v", upgradedCfg)
	}

	runs, err := repo.ListTokenMaintenanceRuns(ctx, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("expected M6 run history preserved, got %+v (err=%v)", runs, err)
	}
	acc, err := repo.GetActive(ctx)
	if err != nil || acc == nil || acc.MSISDN != "6281900000001" {
		t.Fatalf("expected M6 account preserved, got %+v (err=%v)", acc, err)
	}
}

func TestN3_DestinationValidationAndDeduplication(t *testing.T) {
	validAll := []NotificationDestination{
		{ID: "owner", Type: DestOwner, Label: "Owner", Enabled: true},
		{ID: "glog", Type: DestGlobalLog, Label: "Log", Enabled: true},
		{ID: "grp", Type: DestGroup, Label: "Ops Group", Enabled: true, PeerID: -123456},
		{ID: "sgrp", Type: DestSupergroup, Label: "Admin SG", Enabled: true, PeerID: -1009876543210, AccessHash: 111},
		{ID: "bot", Type: DestBotPM, Label: "Alert Bot", Enabled: false, PeerID: 55555, AccessHash: 222},
	}
	norm, err := NormalizeAndValidateDestinations(validAll)
	if err != nil {
		t.Fatalf("expected valid 5 destinations, got err=%v", err)
	}
	if norm[2].PeerID != 123456 {
		t.Fatalf("expected basic group peer_id normalized to positive 123456, got %d", norm[2].PeerID)
	}
	if norm[3].PeerID != 9876543210 {
		t.Fatalf("expected supergroup -100 prefix stripped to 9876543210, got %d", norm[3].PeerID)
	}

	// Channel also valid
	if _, err := NormalizeAndValidateDestination(NotificationDestination{
		ID: "ch-1", Type: DestChannel, Label: "Ch", Enabled: true, PeerID: 777888, AccessHash: 333,
	}); err != nil {
		t.Fatalf("expected valid channel, got %v", err)
	}

	// Empty list valid (implicit Owner PM fallback)
	if emptyNorm, err := NormalizeAndValidateDestinations(nil); err != nil || len(emptyNorm) != 0 {
		t.Fatalf("expected empty list valid, got %+v err=%v", emptyNorm, err)
	}

	// Exceeding MaxNotificationDestinations (5) rejected explicitly
	tooMany := append(append([]NotificationDestination(nil), validAll...), NotificationDestination{
		ID: "ch-extra", Type: DestChannel, Label: "Extra", Enabled: true, PeerID: 999, AccessHash: 444,
	})
	if _, err := NormalizeAndValidateDestinations(tooMany); err == nil {
		t.Fatal("expected error for >5 destinations")
	}

	invalidCases := []struct {
		name string
		dest NotificationDestination
	}{
		{"unknown_type", NotificationDestination{ID: "x", Type: "webhook"}},
		{"empty_id", NotificationDestination{ID: "  ", Type: DestOwner}},
		{"owner_with_peer", NotificationDestination{ID: "o", Type: DestOwner, PeerID: 1}},
		{"global_log_with_hash", NotificationDestination{ID: "g", Type: DestGlobalLog, AccessHash: 1}},
		{"group_zero_peer", NotificationDestination{ID: "grp", Type: DestGroup, PeerID: 0}},
		{"group_with_100_prefix", NotificationDestination{ID: "grp", Type: DestGroup, PeerID: -1001234}},
		{"group_with_hash", NotificationDestination{ID: "grp", Type: DestGroup, PeerID: 123, AccessHash: 99}},
		{"supergroup_missing_hash", NotificationDestination{ID: "sg", Type: DestSupergroup, PeerID: 123}},
		{"channel_missing_hash", NotificationDestination{ID: "ch", Type: DestChannel, PeerID: 123}},
		{"bot_missing_hash", NotificationDestination{ID: "b", Type: DestBotPM, PeerID: 123}},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NormalizeAndValidateDestination(tc.dest); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}

	// Equivalent supergroup representations deduplicated
	if _, err := NormalizeAndValidateDestinations([]NotificationDestination{
		{ID: "sg1", Type: DestSupergroup, PeerID: -1001122334455, AccessHash: 10},
		{ID: "sg2", Type: DestSupergroup, PeerID: 1122334455, AccessHash: 10},
	}); err == nil {
		t.Fatal("expected duplicate supergroup identity to be rejected")
	}
	if _, err := NormalizeAndValidateDestinations([]NotificationDestination{
		{ID: "o1", Type: DestOwner},
		{ID: "o2", Type: DestOwner},
	}); err == nil {
		t.Fatal("expected duplicate owner destination to be rejected")
	}
}

func TestN3_EncodeDecodeJSON_AndRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, repo := openMyXLTestDB(t)

	dests := []NotificationDestination{
		{ID: "owner", Type: DestOwner, Label: "Owner", Enabled: true},
		{ID: "ch", Type: DestChannel, Label: "Alerts", Enabled: true, PeerID: -1004455667788, AccessHash: 999},
	}
	encoded1, err := EncodeNotificationDestinations(dests)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	encoded2, err := EncodeNotificationDestinations(dests)
	if err != nil || encoded1 != encoded2 {
		t.Fatalf("expected deterministic encoding, got %q vs %q (err=%v)", encoded1, encoded2, err)
	}
	decoded, err := DecodeNotificationDestinations(encoded1)
	if err != nil || len(decoded) != 2 || decoded[1].PeerID != 4455667788 {
		t.Fatalf("unexpected decoded: %+v (err=%v)", decoded, err)
	}

	// Corrupt/invalid JSON must fail explicitly without returning empty list
	for _, bad := range []string{"", "null", "{bad", `[{"id":"x","type":"unknown"}]`, `[{"id":"sg","type":"supergroup","peer_id":1}]`} {
		if _, err := DecodeNotificationDestinations(bad); err == nil {
			t.Fatalf("expected decode error for %q", bad)
		}
	}

	// Repository round-trip
	cfg := DefaultTokenMaintenanceConfig()
	cfg.Enabled = true
	cfg.Interval = 12 * time.Hour
	cfg.NotifyOnPartialFailure = false
	cfg.Destinations = dests
	if err := repo.SaveTokenMaintenanceConfig(ctx, &cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	loaded, err := repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.NotifyOnPartialFailure || len(loaded.Destinations) != 2 || loaded.Interval != 12*time.Hour {
		t.Fatalf("loaded config mismatch: %+v", loaded)
	}

	// Empty destinations saved as []
	cfg.Destinations = nil
	if err := repo.SaveTokenMaintenanceConfig(ctx, &cfg); err != nil {
		t.Fatalf("save empty destinations: %v", err)
	}
	var rawJSON string
	if err := db.QueryRowContext(ctx, `SELECT destinations_json FROM myxl_token_maintenance_config WHERE id = 1`).Scan(&rawJSON); err != nil {
		t.Fatal(err)
	}
	if rawJSON != "[]" {
		t.Fatalf("expected destinations_json = '[]', got %q", rawJSON)
	}

	// Corrupt DB JSON fails explicitly on GetTokenMaintenanceConfig
	if _, err := db.ExecContext(ctx, `UPDATE myxl_token_maintenance_config SET destinations_json = '{corrupt' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetTokenMaintenanceConfig(ctx); err == nil || !strings.Contains(err.Error(), "decode notification destinations") {
		t.Fatalf("expected explicit decode error on corrupt DB JSON, got %v", err)
	}
}
