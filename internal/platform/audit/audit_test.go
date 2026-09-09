package audit

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
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
