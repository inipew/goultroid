package plugin

import (
	"errors"
	"testing"
)

func TestCapabilityGate_NormalAndPrivileged(t *testing.T) {
	gate := NewCapabilityGate()

	manifest := Manifest{
		ID:           "downloader",
		Name:         "Downloader",
		Version:      "1.0.0",
		Capabilities: []string{CapHTTP, CapFilesystemTemp, CapProcessExecute},
	}

	if err := gate.RegisterManifest(manifest); err != nil {
		t.Fatalf("register manifest failed: %v", err)
	}

	// Normal declared capability should pass
	if err := gate.Check("downloader", CapHTTP); err != nil {
		t.Errorf("expected CapHTTP allowed, got: %v", err)
	}

	// Undeclared capability should be denied
	err := gate.Check("downloader", CapTelegramRaw)
	if !errors.Is(err, ErrCapabilityDenied) {
		t.Errorf("expected undeclared capability to be denied, got: %v", err)
	}

	// Privileged capability without allowlist should be denied
	err = gate.Check("downloader", CapProcessExecute)
	if !errors.Is(err, ErrCapabilityDenied) {
		t.Errorf("expected unallowlisted privileged capability to be denied, got: %v", err)
	}

	// Allow privileged capability
	gate.AllowPrivileged("downloader", CapProcessExecute)
	if err := gate.Check("downloader", CapProcessExecute); err != nil {
		t.Errorf("expected allowlisted privileged capability to pass, got: %v", err)
	}
}
