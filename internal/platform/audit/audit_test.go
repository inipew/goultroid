package audit

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestAuditService_RecordAndRecent(t *testing.T) {
	svc := NewService(zap.NewNop(), 3)

	ctx := core.WithCorrelationID(context.Background(), "cid-audit-test")

	e1 := AuditEvent{Action: "sudo.add", ActorID: 100, Target: "200"}
	e2 := AuditEvent{Action: "plugin.disable", ActorID: 100, Target: "weather"}
	e3 := AuditEvent{Action: "user.ban", ActorID: 100, Target: "300"}
	e4 := AuditEvent{Action: "config.set", ActorID: 100, Target: "prefix"}

	if err := svc.Record(ctx, e1); err != nil {
		t.Fatal(err)
	}
	if err := svc.Record(ctx, e2); err != nil {
		t.Fatal(err)
	}
	if err := svc.Record(ctx, e3); err != nil {
		t.Fatal(err)
	}
	// Buffer full, this should evict e1
	if err := svc.Record(ctx, e4); err != nil {
		t.Fatal(err)
	}

	recent := svc.Recent(10)
	if len(recent) != 3 {
		t.Fatalf("expected 3 events in ring buffer, got %d", len(recent))
	}

	// Newest first: e4, then e3, then e2
	if recent[0].Action != "config.set" {
		t.Errorf("expected recent[0] to be config.set, got %s", recent[0].Action)
	}
	if recent[0].CorrelationID != "cid-audit-test" {
		t.Errorf("expected correlation ID propagated, got %s", recent[0].CorrelationID)
	}
	if recent[2].Action != "plugin.disable" {
		t.Errorf("expected recent[2] to be plugin.disable, got %s", recent[2].Action)
	}
}

func TestA6AuditDetailsAreSafeBoundedAndImmutable(t *testing.T) {
	const secret = "private-token-never-log"
	coreLogger, observed := observer.New(zap.InfoLevel)
	svc := NewService(zap.New(coreLogger), 2)
	details := map[string]any{
		"found":          true,
		"arg_count":      2,
		"owner_present":  true,
		"secret_present": true,
		"args":           []string{secret},
		"owner":          secret,
		"redacted":       secret,
		"unknown_nested": map[string]any{"text": secret},
		"unexpected":     errorsContainingSecret(secret),
	}
	ctx := core.WithCorrelationID(context.Background(), "audit-privacy-test")
	if err := svc.Record(ctx, AuditEvent{Action: "process.execute", Target: "echo", Details: details}); err != nil {
		t.Fatal(err)
	}
	details["arg_count"] = 999
	details["found"] = false
	details["args"] = []string{"changed"}
	recent := svc.Recent(1)
	if len(recent) != 1 {
		t.Fatalf("missing audit event: %v", recent)
	}
	if got := recent[0].Details; len(got) != 4 || got["arg_count"] != 2 || got["found"] != true {
		t.Fatalf("metadata not sanitized and snapshotted: %v", got)
	}
	recent[0].Details["arg_count"] = -1
	if got := svc.Recent(1)[0].Details["arg_count"]; got != 2 {
		t.Fatalf("caller modified retained audit metadata: %v", got)
	}
	for _, entry := range observed.All() {
		if strings.Contains(fmt.Sprint(entry.Message, entry.ContextMap()), secret) {
			t.Fatalf("structured log leaked secret: %v", entry.ContextMap())
		}
	}
}

type a6SecretError struct{ value string }

func (e a6SecretError) Error() string { return e.value }

func errorsContainingSecret(value string) error { return a6SecretError{value: value} }

func TestA6AuditDetailsRejectNonScalarAndWrongType(t *testing.T) {
	svc := NewService(zap.NewNop(), 2)
	if err := svc.Record(context.Background(), AuditEvent{
		Action: "audit.type.boundary",
		Details: map[string]any{
			"found":          "true",
			"arg_count":      -1,
			"owner_present":  []bool{true},
			"secret_present": map[string]any{"secret": "hidden"},
			"raw_text":       "hidden",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got := svc.Recent(1)[0].Details; got != nil {
		t.Fatalf("unexpected data passed audit allowlist: %v", got)
	}
}
