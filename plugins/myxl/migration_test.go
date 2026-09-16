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
}
