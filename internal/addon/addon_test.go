package addon_test

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/addon"
	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

func setupTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestManifest_ParseAndValidate(t *testing.T) {
	// 1. Valid YAML manifest
	validYAML := `
name: sample-addon
version: 1.2.0
min_goultroid: 0.5.0
description: A useful helper addon
author: Pew
commands:
  - helper
  - assist
capabilities:
  - telegram.send
  - media.download
`
	m, err := addon.ParseManifest([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}
	if m.Name != "sample-addon" || m.Version != "1.2.0" || len(m.Commands) != 2 || len(m.Capabilities) != 2 {
		t.Errorf("unexpected parsed manifest: %+v", m)
	}

	// 2. Invalid name (too short or special characters)
	invalidNameYAML := `
name: a!
version: 1.0.0
commands: [test]
`
	if _, err := addon.ParseManifest([]byte(invalidNameYAML)); err == nil {
		t.Fatalf("expected error on invalid name, got nil")
	}

	// 3. Missing commands
	noCmdYAML := `
name: no-cmd
version: 1.0.0
`
	if _, err := addon.ParseManifest([]byte(noCmdYAML)); err == nil {
		t.Fatalf("expected error on missing commands, got nil")
	}

	// 4. Invalid capability
	badCapYAML := `
name: bad-cap
version: 1.0.0
commands: [test]
capabilities:
  - root.takeover
`
	if _, err := addon.ParseManifest([]byte(badCapYAML)); err == nil {
		t.Fatalf("expected error on invalid capability, got nil")
	}
}

func TestManifest_CheckCompatibility(t *testing.T) {
	m := &addon.Manifest{
		Name:         "compat-test",
		Version:      "1.0.0",
		MinGoUltroid: "1.2.0",
	}

	// Compatible
	if err := addon.CheckCompatibility(m, "1.2.0"); err != nil {
		t.Errorf("expected 1.2.0 to be compatible: %v", err)
	}
	if err := addon.CheckCompatibility(m, "v1.3.0"); err != nil {
		t.Errorf("expected v1.3.0 to be compatible: %v", err)
	}
	if err := addon.CheckCompatibility(m, "2.0.0"); err != nil {
		t.Errorf("expected 2.0.0 to be compatible: %v", err)
	}

	// Incompatible
	if err := addon.CheckCompatibility(m, "1.1.9"); err == nil {
		t.Errorf("expected 1.1.9 to be incompatible with min 1.2.0, got nil")
	}
	if err := addon.CheckCompatibility(m, "0.9.0"); err == nil {
		t.Errorf("expected 0.9.0 to be incompatible, got nil")
	}
}

func TestCapabilityGate_Enforcement(t *testing.T) {
	gate := addon.NewCapabilityGate()

	addonName := "downloader-pro"
	gate.Register(addonName, []addon.Capability{
		addon.CapMediaDownload,
		addon.CapStorageWrite,
	})

	// Authorized capabilities
	if !gate.HasCapability(addonName, addon.CapMediaDownload) {
		t.Errorf("expected CapMediaDownload to be authorized")
	}
	if err := gate.Assert(addonName, addon.CapMediaDownload); err != nil {
		t.Errorf("Assert failed for authorized cap: %v", err)
	}

	// Unauthorized capability
	if gate.HasCapability(addonName, addon.CapProcessExecute) {
		t.Errorf("CapProcessExecute should not be authorized")
	}
	if err := gate.Assert(addonName, addon.CapProcessExecute); err == nil {
		t.Errorf("expected error asserting unauthorized capability, got nil")
	}

	// Unregister
	gate.Unregister(addonName)
	if gate.HasCapability(addonName, addon.CapMediaDownload) {
		t.Errorf("expected all capabilities revoked after unregister")
	}
}

func TestManager_Lifecycle(t *testing.T) {
	db := setupTestDB(t)
	repo := addon.NewSQLiteRepository(db)
	gate := addon.NewCapabilityGate()
	mgr := addon.NewManager(repo, gate, "1.5.0", zap.NewNop())
	ctx := context.Background()

	manifestContent := `
name: quote-generator
version: 1.0.0
min_goultroid: 1.0.0
description: Generates aesthetic quotes
author: Bob
commands:
  - quote
capabilities:
  - telegram.send
`

	// 1. Install
	m, err := mgr.Install(ctx, []byte(manifestContent), "https://github.com/example/quotes")
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	if m.Name != "quote-generator" {
		t.Errorf("unexpected manifest name: %s", m.Name)
	}

	// Gate should have telegram.send
	if !gate.HasCapability("quote-generator", addon.CapTelegramSend) {
		t.Fatalf("expected capability telegram.send granted in gate")
	}

	// Duplicate install should fail
	if _, err := mgr.Install(ctx, []byte(manifestContent), ""); err == nil {
		t.Fatalf("expected error on duplicate install, got nil")
	}

	// 2. List & Get
	list, err := mgr.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List returned unexpected: len=%d, err=%v", len(list), err)
	}
	rec, err := mgr.Get(ctx, "quote-generator")
	if err != nil || rec == nil || rec.Author != "Bob" {
		t.Fatalf("Get returned unexpected: %+v (err=%v)", rec, err)
	}

	// 3. Disable
	if err := mgr.Disable(ctx, "quote-generator"); err != nil {
		t.Fatalf("Disable failed: %v", err)
	}
	if gate.HasCapability("quote-generator", addon.CapTelegramSend) {
		t.Fatalf("expected capability revoked after disable")
	}
	rec, _ = mgr.Get(ctx, "quote-generator")
	if rec.Status != string(addon.StatusDisabled) {
		t.Errorf("expected disabled status, got %s", rec.Status)
	}

	// 4. Enable
	if err := mgr.Enable(ctx, "quote-generator"); err != nil {
		t.Fatalf("Enable failed: %v", err)
	}
	if !gate.HasCapability("quote-generator", addon.CapTelegramSend) {
		t.Fatalf("expected capability restored after enable")
	}

	// 5. Uninstall
	if err := mgr.Uninstall(ctx, "quote-generator"); err != nil {
		t.Fatalf("Uninstall failed: %v", err)
	}
	if gate.HasCapability("quote-generator", addon.CapTelegramSend) {
		t.Fatalf("expected capability revoked after uninstall")
	}
	rec, err = mgr.Get(ctx, "quote-generator")
	if rec != nil {
		t.Errorf("expected nil after uninstall, got %+v", rec)
	}
}

func TestManager_ShutdownState(t *testing.T) {
	db := setupTestDB(t)
	repo := addon.NewSQLiteRepository(db)
	gate := addon.NewCapabilityGate()
	mgr := addon.NewManager(repo, gate, "1.5.0", zap.NewNop())
	ctx := context.Background()

	if err := mgr.ShutdownRuntimes(); err != nil {
		t.Fatalf("ShutdownRuntimes failed: %v", err)
	}

	// Should reject StartRuntime after shutdown
	if err := mgr.StartRuntime(ctx, "nonexistent", "/bin/sh", ""); err == nil {
		t.Errorf("expected error starting runtime after shutdown, got nil")
	}

	// Should reject CallRuntime after shutdown
	if _, err := mgr.CallRuntime(ctx, "nonexistent", "hello", nil); err == nil {
		t.Errorf("expected error calling runtime after shutdown, got nil")
	}
}
