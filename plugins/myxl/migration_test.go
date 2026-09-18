package myxl

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

func TestMyXLFeatureMigrationFreshDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("run myxl feature migration: %v", err)
	}

	repo := NewSQLiteRepository(db)
	reserved, err := repo.ReservePurchase(ctx, "daily-key", "6281900000001", "OPT-1", "balance")
	if err != nil || !reserved {
		t.Fatalf("reserve first purchase: reserved=%v err=%v", reserved, err)
	}
	reserved, err = repo.ReservePurchase(ctx, "daily-key", "6281900000001", "OPT-1", "balance")
	if err != nil || reserved {
		t.Fatalf("duplicate purchase reservation must be rejected: reserved=%v err=%v", reserved, err)
	}
	if err := repo.FinishPurchase(ctx, "daily-key", "SUCCESS", "TRX-1", "ok"); err != nil {
		t.Fatalf("finish purchase: %v", err)
	}

	acc1 := &Account{
		MSISDN:       "6281900000001",
		Alias:        "Primary",
		AccessToken:  "token1",
		IDToken:      "idtoken1",
		RefreshToken: "refreshtoken1",
	}
	if err := repo.Save(ctx, acc1); err != nil {
		t.Fatalf("save account 1: %v", err)
	}

	// First account should be active by default
	active, err := repo.GetActive(ctx)
	if err != nil {
		t.Fatalf("get active account: %v", err)
	}
	if active == nil || active.MSISDN != "6281900000001" {
		t.Fatalf("expected account 1 active, got: %#v", active)
	}

	// Save second account
	acc2 := &Account{
		MSISDN: "6281900000002",
		Alias:  "Secondary",
	}
	if err := repo.Save(ctx, acc2); err != nil {
		t.Fatalf("save account 2: %v", err)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(list))
	}
	if _, err := db.ExecContext(ctx, "UPDATE myxl_accounts SET is_active = 1 WHERE msisdn = ?", acc2.MSISDN); err == nil {
		t.Fatal("expected unique active-account invariant to reject a second active account")
	}

	// Switch active to acc2
	if err := repo.SetActive(ctx, "Secondary"); err != nil {
		t.Fatalf("set active account by alias: %v", err)
	}

	active, err = repo.GetActive(ctx)
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if active == nil || active.MSISDN != "6281900000002" {
		t.Fatalf("expected account 2 active, got: %#v", active)
	}

	// Delete acc2, acc1 should become active
	if err := repo.Delete(ctx, "6281900000002"); err != nil {
		t.Fatalf("delete account 2: %v", err)
	}

	listAfter, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(listAfter) != 1 {
		t.Fatalf("expected 1 account after delete, got %d", len(listAfter))
	}

	activeAfter, err := repo.GetActive(ctx)
	if err != nil {
		t.Fatalf("get active after delete: %v", err)
	}
	if activeAfter == nil || activeAfter.MSISDN != "6281900000001" {
		t.Fatalf("expected account 1 to be promoted to active, got: %#v", activeAfter)
	}

	// Test SetAlias
	if err := repo.SetAlias(ctx, "6281900000001", "WorkSIM"); err != nil {
		t.Fatalf("set alias failed: %v", err)
	}
	updated, err := repo.GetByMSISDN(ctx, "WorkSIM")
	if err != nil || updated == nil || updated.Alias != "WorkSIM" {
		t.Fatalf("expected account retrieved by new alias WorkSIM, got: %#v (err: %v)", updated, err)
	}

	// Test Saved Packages
	pkg := &SavedPackage{
		MSISDN:     "6281900000001",
		OptionCode: "OPT-12345",
		Name:       "Xtra Combo 10GB",
		Price:      50000,
		FamilyCode: "FAM-999",
	}
	if err := repo.SavePackage(ctx, pkg); err != nil {
		t.Fatalf("save package: %v", err)
	}
	savedList, err := repo.GetSavedPackages(ctx, "6281900000001")
	if err != nil || len(savedList) != 1 {
		t.Fatalf("expected 1 saved package, got %d (err: %v)", len(savedList), err)
	}
	singlePkg, err := repo.GetSavedPackage(ctx, "6281900000001", "OPT-12345")
	if err != nil || singlePkg == nil || singlePkg.Name != "Xtra Combo 10GB" {
		t.Fatalf("get single package failed: %#v (err: %v)", singlePkg, err)
	}
	if err := repo.DeleteSavedPackage(ctx, "6281900000001", "OPT-12345"); err != nil {
		t.Fatalf("delete saved package: %v", err)
	}
	savedListAfter, err := repo.GetSavedPackages(ctx, "6281900000001")
	if err != nil || len(savedListAfter) != 0 {
		t.Fatalf("expected 0 saved packages after delete, got %d", len(savedListAfter))
	}

	// Test Decoy Configs (seeded from migration003)
	decoy, err := repo.GetDecoy(ctx, "default-balance")
	if err != nil || decoy == nil {
		t.Fatalf("expected seeded decoy 'default-balance', got nil (err: %v)", err)
	}
	if decoy.Price != 889750 || !decoy.IsEnterprise {
		t.Fatalf("unexpected decoy values: %#v", decoy)
	}
	decoy.OptionCode = "OPT-DECOY-777"
	decoy.TokenConfirmation = "CONFIRM-TOKEN-XYZ"
	decoy.LastFetchedAt = 1234567890
	if err := repo.UpsertDecoy(ctx, decoy); err != nil {
		t.Fatalf("upsert decoy: %v", err)
	}
	updatedDecoy, err := repo.GetDecoy(ctx, "default-balance")
	if err != nil || updatedDecoy == nil || updatedDecoy.OptionCode != "OPT-DECOY-777" {
		t.Fatalf("expected updated decoy with option code, got %#v (err: %v)", updatedDecoy, err)
	}

	// Test TokenExpiresAt persistence (migration004)
	targetTime := time.Date(2026, 10, 15, 12, 30, 0, 0, time.UTC)
	acc1.TokenExpiresAt = targetTime
	if err := repo.Save(ctx, acc1); err != nil {
		t.Fatalf("save account with TokenExpiresAt: %v", err)
	}
	fetched, err := repo.GetByMSISDN(ctx, "6281900000001")
	if err != nil || fetched == nil {
		t.Fatalf("get account 1 failed: %v", err)
	}
	if !fetched.TokenExpiresAt.Equal(targetTime) {
		t.Fatalf("expected TokenExpiresAt %v, got %v", targetTime, fetched.TokenExpiresAt)
	}
}

func TestMigration006RepairsMultipleActiveAccounts(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE myxl_accounts (
			msisdn TEXT PRIMARY KEY,
			is_active INTEGER NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);
		INSERT INTO myxl_accounts VALUES
			('6281900000001', 1, '2026-01-01 00:00:00', '2026-01-01 00:00:00'),
			('6281900000002', 1, '2026-01-02 00:00:00', '2026-01-03 00:00:00');
	`); err != nil {
		t.Fatalf("prepare legacy accounts: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (migration006{}).Up(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply migration006: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit migration006: %v", err)
	}

	var activeCount int
	var activeMSISDN string
	if err := db.QueryRowContext(ctx, "SELECT count(*), max(msisdn) FROM myxl_accounts WHERE is_active = 1").Scan(&activeCount, &activeMSISDN); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 || activeMSISDN != "6281900000002" {
		t.Fatalf("expected newest account to be the sole active account, count=%d msisdn=%s", activeCount, activeMSISDN)
	}
	if _, err := db.ExecContext(ctx, "UPDATE myxl_accounts SET is_active = 1 WHERE msisdn = '6281900000001'"); err == nil {
		t.Fatal("expected unique index to reject a second active account")
	}
}
