package myxl

import (
	"context"
	"testing"

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
}
