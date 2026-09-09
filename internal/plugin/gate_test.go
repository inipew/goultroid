package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/platform/audit"
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

func TestCapabilityGate_FailClosed(t *testing.T) {
	gate := NewCapabilityGate()
	gate.SetFailClosed(true)

	// Plugin without manifest should be denied in fail-closed mode
	err := gate.Check("unregistered", CapHTTP)
	if !errors.Is(err, ErrCapabilityDenied) {
		t.Errorf("expected unregistered plugin to be denied in fail-closed mode, got: %v", err)
	}

	// Once registered, declared capabilities should pass
	gate.Register("registered", []string{CapHTTP})
	if err := gate.Check("registered", CapHTTP); err != nil {
		t.Errorf("expected declared capability to pass, got: %v", err)
	}
}

type mockAuditor struct {
	events []audit.AuditEvent
}

func (m *mockAuditor) Record(ctx context.Context, event audit.AuditEvent) error {
	m.events = append(m.events, event)
	return nil
}

func (m *mockAuditor) Recent(limit int) []audit.AuditEvent {
	return m.events
}

func TestCapabilityGate_Auditor(t *testing.T) {
	gate := NewCapabilityGate()
	aud := &mockAuditor{}
	gate.SetAuditor(aud)

	gate.Register("testplug", []string{CapHTTP, CapProcessExecute})
	_ = gate.Check("testplug", CapHTTP)
	_ = gate.Check("testplug", CapProcessExecute) // denied because privileged not allowlisted

	if len(aud.events) != 2 {
		t.Fatalf("expected 2 audit events, got %d", len(aud.events))
	}
	if aud.events[0].Action != "capability.granted" {
		t.Errorf("expected first action capability.granted, got %s", aud.events[0].Action)
	}
	if aud.events[1].Action != "capability.denied" {
		t.Errorf("expected second action capability.denied, got %s", aud.events[1].Action)
	}
}
