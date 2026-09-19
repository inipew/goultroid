package addon_test

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/addon"
	"github.com/inipew/goultroid/internal/database"
)

func TestSQLiteRepository(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	repo := addon.NewSQLiteRepository(db)
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema failed: %v", err)
	}

	addons, err := repo.ListAddons(ctx)
	if err != nil {
		t.Fatalf("ListAddons failed: %v", err)
	}
	if len(addons) != 0 {
		t.Fatalf("expected 0 addons initially, got %d", len(addons))
	}

	a1 := &addon.AddonRecord{
		Name:         "weather-addon",
		Version:      "1.0.0",
		Description:  "Shows current weather",
		Author:       "Alice",
		SourceURL:    "https://example.com/weather",
		Status:       "active",
		Capabilities: "network.http,telegram.send",
		MinVersion:   "0.9.0",
		InstalledAt:  time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	if err := repo.SaveAddon(ctx, a1); err != nil {
		t.Fatalf("SaveAddon failed: %v", err)
	}

	contract := addon.AddonContract{
		Commands: []string{"weather"},
		Events:   []addon.EventType{addon.EventMessageCreated, addon.EventReactionUpdated},
	}
	if err := repo.SaveContract(ctx, a1.Name, contract); err != nil {
		t.Fatalf("SaveContract failed: %v", err)
	}
	gotContract, err := repo.GetContract(ctx, a1.Name)
	if err != nil {
		t.Fatalf("GetContract failed: %v", err)
	}
	if len(gotContract.Commands) != 1 || gotContract.Commands[0] != "weather" ||
		len(gotContract.Events) != 2 || gotContract.Events[1] != addon.EventReactionUpdated {
		t.Fatalf("unexpected contract: %+v", gotContract)
	}

	got, err := repo.GetAddon(ctx, "weather-addon")
	if err != nil || got == nil {
		t.Fatalf("GetAddon failed: %v", err)
	}
	if got.Author != "Alice" || got.Version != "1.0.0" {
		t.Errorf("unexpected addon data: %+v", got)
	}

	// Case-insensitive retrieval
	got2, err := repo.GetAddon(ctx, "WEATHER-ADDON")
	if err != nil || got2 == nil {
		t.Fatalf("GetAddon case-insensitive failed: %v", err)
	}
	if got2.Name != "weather-addon" {
		t.Errorf("expected cleanName weather-addon, got %s", got2.Name)
	}

	if err := repo.SetAddonStatus(ctx, "weather-addon", "disabled"); err != nil {
		t.Fatalf("SetAddonStatus failed: %v", err)
	}
	gotDisabled, _ := repo.GetAddon(ctx, "weather-addon")
	if gotDisabled.Status != "disabled" {
		t.Errorf("expected disabled, got %s", gotDisabled.Status)
	}

	if err := repo.DeleteAddon(ctx, "weather-addon"); err != nil {
		t.Fatalf("DeleteAddon failed: %v", err)
	}
	if err := repo.DeleteContract(ctx, "weather-addon"); err != nil {
		t.Fatalf("DeleteContract failed: %v", err)
	}
	deletedContract, err := repo.GetContract(ctx, "weather-addon")
	if err != nil {
		t.Fatalf("GetContract after delete: %v", err)
	}
	if len(deletedContract.Commands) != 0 || len(deletedContract.Events) != 0 {
		t.Fatalf("contract remained after delete: %+v", deletedContract)
	}
	gotDeleted, _ := repo.GetAddon(ctx, "weather-addon")
	if gotDeleted != nil {
		t.Errorf("expected nil after delete, got %+v", gotDeleted)
	}
}
