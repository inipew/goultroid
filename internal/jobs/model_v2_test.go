package jobs

import (
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestJobIdentity_SeparatesOccurrenceAttemptAndTask(t *testing.T) {
	now := time.Now().UTC()
	occurrence, err := NewJobOccurrence("occ-1", "job-1", OccurrenceScheduled, now, now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewJobAttempt("attempt-1", occurrence.ID(), "task-1", 1, 1, AttemptPrepared)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewJobAttempt("attempt-2", occurrence.ID(), "task-2", 2, 2, AttemptPrepared)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() == second.ID() || first.TaskID() == second.TaskID() {
		t.Fatal("retry reused attempt or task identity")
	}
	if first.OccurrenceID() != second.OccurrenceID() {
		t.Fatal("retry must remain linked to the same occurrence")
	}
}

func TestPayloadRegistry_RejectsUnknownVersionAndOversize(t *testing.T) {
	registry := NewPayloadRegistry()
	if err := registry.Register(PayloadDescriptor{Kind: "telegram.command", Version: 1, MaxBytes: 4}); err != nil {
		t.Fatal(err)
	}
	unknown, _ := tasks.NewPayloadRef("telegram.command", 2, []byte("ok"))
	if err := registry.Validate(unknown); err == nil {
		t.Fatal("expected unknown payload version to be rejected")
	}
	large, _ := tasks.NewPayloadRef("telegram.command", 1, []byte("12345"))
	if err := registry.Validate(large); err == nil {
		t.Fatal("expected oversized payload to be rejected")
	}
	valid, _ := tasks.NewPayloadRef("telegram.command", 1, []byte("1234"))
	if err := registry.Validate(valid); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
}
